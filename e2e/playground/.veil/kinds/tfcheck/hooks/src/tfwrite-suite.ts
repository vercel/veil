import type { FS, RenderHook, RenderHookContext } from './veil-types';

// The Go suite for pkg/tfwrite, run again in JavaScript against the same
// fixtures. Go proving the tree is correct says nothing about the class
// layer over it, and the two sides can drift: the JS holds its own copy
// of the label positions, the block-type mapping and the dirty marking.
//
// Every check throws on failure, so a break fails the render rather than
// a comparison somewhere downstream. The count is written out and
// asserted by the e2e, so a check silently not running is also a failure.

let checks = 0;

function ok(cond: unknown, what: string): void {
  checks++;
  if (!cond) throw new Error(`tfwrite-js: ${what}`);
}

function eq(got: unknown, want: unknown, what: string): void {
  checks++;
  if (got !== want) {
    throw new Error(`tfwrite-js: ${what}\n  got:  ${JSON.stringify(got)}\n  want: ${JSON.stringify(want)}`);
  }
}

function count(haystack: string, needle: string): number {
  return haystack.split(needle).length - 1;
}

const suite: RenderHook = {
  render(ctx: RenderHookContext, fs: FS): FS {
    const tfmod = ctx.std.terraform;
    const messySrc = String(fs.get('files/messy.tf')!.getContent());
    const everySrc = String(fs.get('files/everyblock.tf')!.getContent());

    // --- untouched file is byte-identical -------------------------------
    eq(tfmod.stringify(tfmod.parse(messySrc)), messySrc, 'untouched messy.tf round trip');
    eq(tfmod.stringify(tfmod.parse(everySrc)), everySrc, 'untouched everyblock.tf round trip');

    // --- comments are placed with their item -----------------------------
    {
      const f = tfmod.parse(messySrc);
      const top = f.comments().map((c) => c.text());
      eq(top.length, 2, 'file-level comment count');
      eq(top[0], '# Top of file.', 'first file-level comment');
      eq(top[1], '# The bucket holds build logs.', 'second file-level comment');

      const inner = f.resource('aws_s3_bucket', 'logs')!.comments().map((c) => c.text());
      eq(inner.length, 2, 'comments inside the bucket block');
      ok(inner[1].indexOf('Nested comment') >= 0, 'nested comment belongs to its block');
    }

    // --- an edit is local -------------------------------------------------
    {
      const f = tfmod.parse(messySrc);
      f.module('network')!.setAttribute('source', '"./modules/vpc"');
      const out = tfmod.stringify(f);
      ok(out.indexOf('source = "./modules/vpc"') >= 0, 'edited attribute');
      ok(out.indexOf('./modules/network') < 0, 'old value gone');
      ok(out.indexOf('bucket =    "acme-logs-${var.environment}"   # odd spacing on purpose') >= 0,
        'untouched odd spacing survives an edit elsewhere');
      ok(out.indexOf('# Top of file.') >= 0, 'comments survive');
      ok(out.indexOf('# Nested comment.') >= 0, 'nested comments survive');
    }

    // --- rename keeps the body -------------------------------------------
    {
      const f = tfmod.parse(messySrc);
      f.resource('aws_s3_bucket', 'logs')!.setName('build_logs');
      const out = tfmod.stringify(f);
      ok(out.indexOf('resource "aws_s3_bucket" "build_logs" {') >= 0, 'renamed');
      ok(out.indexOf('"logs"') < 0, 'old label gone');
      ok(out.indexOf('# Nested comment.') >= 0, 'body came through the rename');
      ok(out.indexOf('env = var.environment') >= 0, 'nested attribute came through');
    }

    // --- expressions stay source text -------------------------------------
    {
      const f = tfmod.parse(messySrc);
      eq(f.resource('aws_s3_bucket', 'logs')!.attribute('bucket')!.expr(),
        '"acme-logs-${var.environment}"', 'a reference is not evaluated');
      eq(f.terraform()!.attribute('required_version')!.expr(), '">= 1.5"', 'a literal keeps its quotes');
    }

    // --- add and remove ----------------------------------------------------
    {
      const f = tfmod.parse(messySrc);
      f.addOutput('bucket_name').setAttribute('value', 'aws_s3_bucket.logs.id');
      ok(f.module('network')!.removeAttribute('source'), 'removeAttribute reports found');
      const out = tfmod.stringify(f);
      ok(out.indexOf('output "bucket_name" {') >= 0, 'block added');
      ok(out.indexOf('value = aws_s3_bucket.logs.id') >= 0, 'attribute added');
      ok(out.indexOf('./modules/network') < 0, 'attribute removed');
      eq(tfmod.stringify(tfmod.parse(out)), out, 'edited output is stable on reparse');
    }

    // --- each block type has its own labels --------------------------------
    {
      const f = tfmod.parse('locals {}\n');
      const r = f.addResource('aws_s3_bucket', 'logs');
      eq(r.resourceType(), 'aws_s3_bucket', 'resource type label');
      eq(r.name(), 'logs', 'resource name label');
      eq(f.addProvider('aws').name(), 'aws', 'provider takes one label');
      eq(f.addVariable('region').name(), 'region', 'variable name');
      eq(f.locals().length, 1, 'the parsed locals block is its own type');
      const m = f.addModule('network');
      m.setAttribute('source', '"./modules/network"');
      eq(m.source(), './modules/network', 'a modelled block reads its key attribute');

      const out = tfmod.stringify(f);
      ok(out.indexOf('resource "aws_s3_bucket" "logs" {') >= 0, 'resource written');
      ok(out.indexOf('provider "aws" {') >= 0, 'provider written');
      eq(tfmod.stringify(tfmod.parse(out)), out, 'generated output is stable');
    }

    // --- unmodelled blocks stay editable ------------------------------------
    {
      const src = 'resource "aws_instance" "web" {\n  provisioner "local-exec" {\n    command = "echo hi"\n  }\n}\n';
      const f = tfmod.parse(src);
      eq(tfmod.stringify(f), src, 'a file with a nested block round trips');
      const nested = f.resource('aws_instance', 'web')!.blocks();
      eq(nested.length, 1, 'one nested block');
      eq(nested[0].blockType(), 'provisioner', 'nested block type');
      eq(JSON.stringify(nested[0].labels()), '["local-exec"]', 'nested block labels');
    }

    // --- removal actually removes -------------------------------------------
    {
      const src = '# Keep this comment.\nresource "aws_s3_bucket" "a" {\n  bucket = "a"\n}\n\n' +
        '# Drop this comment.\nresource "aws_s3_bucket" "b" {\n  bucket = "b"\n}\n\n' +
        'resource "aws_s3_bucket" "c" {\n  bucket = "c"\n}\n';
      const f = tfmod.parse(src);
      ok(f.resource('aws_s3_bucket', 'b')!.delete(), 'delete a resource in the middle');
      ok(f.comments()[1].delete(), 'delete the comment that described it');
      const out = tfmod.stringify(f);
      ok(out.indexOf('"b"') < 0, 'the resource is gone');
      ok(out.indexOf('Drop this comment') < 0, 'and so is the comment');
      ok(out.indexOf('# Keep this comment.') >= 0, 'the other comment stayed');
      ok(out.indexOf('"a"') >= 0 && out.indexOf('"c"') >= 0, 'neighbours stayed');
      eq(tfmod.stringify(tfmod.parse(out)), out, 'stable after removal');
    }

    // --- removing from the end -----------------------------------------------
    {
      const src = 'resource "aws_s3_bucket" "a" {\n  bucket = "a"\n}\n\n' +
        'resource "aws_s3_bucket" "last" {\n  bucket = "last"\n}\n';
      const f = tfmod.parse(src);
      ok(f.resource('aws_s3_bucket', 'last')!.delete(), 'delete the last block');
      const out = tfmod.stringify(f);
      ok(out.indexOf('last') < 0, 'gone');
      ok(out.slice(-2) === '}\n', 'the file still ends with a newline');
    }

    // --- delete on the item itself ---------------------------------------------
    {
      const src = '# Keep.\nresource "aws_s3_bucket" "a" {\n  bucket   = "a"\n  acl      = "private"\n}\n\n' +
        'variable "region" {\n  default = "us-east-1"\n}\n';
      const f = tfmod.parse(src);
      ok(f.variable('region')!.delete(), 'delete a variable by its own handle');
      ok(f.resource('aws_s3_bucket', 'a')!.attribute('acl')!.delete(), 'delete an attribute');
      const out = tfmod.stringify(f);
      ok(out.indexOf('region') < 0, 'variable gone');
      ok(out.indexOf('acl') < 0, 'attribute gone');
      ok(out.indexOf('bucket   = "a"') >= 0, 'the attribute left behind keeps its alignment');
    }

    // --- a miss is null, and a chain through one is safe --------------------------
    {
      const f = tfmod.parse(messySrc);
      eq(f.resource('nope', 'nope'), null, 'a missing resource is null');
      eq(f.provider('nope'), null, 'a missing provider is null');
      eq(f.variable('nope'), null, 'a missing variable is null');
      eq(f.module('nope')?.attribute('source')?.expr() ?? '', '', 'a chain through a miss yields nothing');
      eq(f.resource('nope', 'nope')?.attribute('acl')?.delete() ?? false, false, 'deleting through a miss');
      eq(tfmod.stringify(f), messySrc, 'none of it changed the file');
    }

    // --- comments inside expressions are not items ---------------------------------
    {
      const src = 'resource "aws_s3_bucket" "logs" {\n  bucket =    "acme-logs"   # trailing comment\n\n' +
        '  # Standalone comment.\n  tags = {\n    env = var.environment   # inside the expression\n  }\n' +
        '  # Last comment\n}\n';
      const f = tfmod.parse(src);
      eq(tfmod.stringify(f), src, 'round trip');
      const texts = f.resource('aws_s3_bucket', 'logs')!.comments().map((c) => c.text());
      eq(JSON.stringify(texts),
        JSON.stringify(['# trailing comment', '# Standalone comment.', '# Last comment']),
        'the one inside the expression belongs to the attribute');

      f.resource('aws_s3_bucket', 'logs')!.setAttribute('bucket', '"changed"');
      const out = tfmod.stringify(f);
      eq(count(out, '# inside the expression'), 1, 'not duplicated by a reprint');
      eq(count(out, '# Last comment'), 1, 'not duplicated');
      ok(out.indexOf('# Last comment\n}') >= 0, 'no blank line crept in before the brace');
    }

    // --- every top-level block type is typed ----------------------------------------
    {
      const f = tfmod.parse(everySrc);
      ok(f.terraform() !== null, 'terraform');
      eq(f.provider('aws')!.name(), 'aws', 'provider');
      eq(f.variable('region')!.name(), 'region', 'variable');
      eq(f.locals().length, 1, 'locals');
      eq(f.resource('aws_instance', 'web')!.resourceType(), 'aws_instance', 'resource type');
      eq(f.resource('aws_instance', 'web')!.name(), 'web', 'resource name');
      eq(f.dataSource('aws_ami', 'ubuntu')!.dataType(), 'aws_ami', 'data');
      ok(f.ephemeral('aws_secretsmanager_secret_version', 'db') !== null, 'ephemeral');
      ok(f.action('aws_lambda_invoke', 'notify') !== null, 'action');
      eq(f.module('network')!.source(), './modules/network', 'module');
      ok(f.output('vpc_id') !== null, 'output');
      ok(f.check('health') !== null, 'check');
      eq(f.moved().length, 1, 'moved');
      eq(f.removed().length, 1, 'removed');
      eq(f.imports().length, 1, 'import');
      eq(f.blocks().length, 14, 'one of every top-level block type');
    }

    // --- nested blocks are reachable and scoped to their parent -----------------------
    {
      const f = tfmod.parse(everySrc);
      const typesIn = (b: ReturnType<typeof f.body>) => b.blocks().map((x) => x.blockType());

      const tfBlocks = typesIn(f.terraform()!.body());
      ok(tfBlocks.indexOf('required_providers') >= 0, 'required_providers nests in terraform');
      ok(tfBlocks.indexOf('backend') >= 0, 'backend nests in terraform');
      ok(tfBlocks.indexOf('cloud') >= 0, 'cloud nests in terraform');
      ok(tfBlocks.indexOf('provider_meta') >= 0, 'provider_meta nests in terraform');

      const res = f.resource('aws_instance', 'web')!.body();
      const resBlocks = typesIn(res);
      ['lifecycle', 'connection', 'provisioner', 'action_trigger'].forEach((t) =>
        ok(resBlocks.indexOf(t) >= 0, `${t} nests in a resource`));

      // Two levels down.
      const lifecycle = res.blocks()[0];
      eq(lifecycle.blockType(), 'lifecycle', 'first nested block');
      eq(lifecycle.blocks()[0].blockType(), 'precondition', 'nested two deep');

      ok(typesIn(f.variable('region')!.body()).indexOf('validation') >= 0, 'validation nests in variable');
      ok(typesIn(f.check('health')!.body()).indexOf('assert') >= 0, 'assert nests in check');

      // Scoping: the data block inside check/health is not a top-level one.
      eq(f.dataSource('http', 'status'), null, 'a nested data block is not top level');
      ok(f.dataSource('aws_ami', 'ubuntu') !== null, 'the top-level one still is');
    }

    // --- editing every block type stays stable -------------------------------------------
    {
      const f = tfmod.parse(everySrc);
      const blocks = f.blocks();
      blocks.forEach((b) => b.setAttribute('veil_touched', '"yes"'));
      const out = tfmod.stringify(f);
      eq(count(out, 'veil_touched'), blocks.length, 'every block took the edit exactly once');
      eq(tfmod.stringify(tfmod.parse(out)), out, 'an edited file is stable on reparse');
      ok(out.indexOf('# File header comment.') >= 0, 'comments survived every reprint');
      ok(out.indexOf('# trailing comment on a local') >= 0, 'trailing comments too');
    }

    // --- building a file from nothing ------------------------------------------------------
    {
      const f = tfmod.parse('');
      const tf = f.addTerraform();
      tf.setAttribute('required_version', '">= 1.5"');
      tf.addBlock('required_providers')
        .setAttribute('aws', '{ source = "hashicorp/aws", version = "~> 5.0" }');
      f.addVariable('region').setAttribute('type', 'string');
      const r = f.addResource('aws_s3_bucket', 'logs');
      r.setAttribute('bucket', '"acme-logs"');
      r.addBlock('lifecycle').setAttribute('prevent_destroy', 'true');
      f.addOutput('id').setAttribute('value', 'aws_s3_bucket.logs.id');

      const out = tfmod.stringify(f);
      ok(out.slice(-2) === '}\n', 'a generated file ends with a newline');
      ok(out.indexOf('}\n\nvariable "region" {') >= 0, 'blank line between top-level blocks');
      ok(out.indexOf('  lifecycle {\n    prevent_destroy = true\n  }') >= 0, 'nested blocks indented');
      eq(tfmod.stringify(tfmod.parse(out)), out, 'generated output parses and is stable');
    }

    fs.add('sources/tfwrite-checks.txt', String(checks));
    return fs;
  },
};

export default suite;
