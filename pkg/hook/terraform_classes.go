package hook

// terraformClassesJS mirrors pkg/tfwrite in the hook runtime: the same
// method names on the same shapes, so what reads one way in Go reads the
// same way here.
//
//	f.Resource("aws_s3_bucket", "logs").SetName("build_logs")   // Go
//	f.resource('aws_s3_bucket', 'logs').setName('build_logs')   // JS
//
// The tree is plain data the whole time. Every lookup walks the items
// array the host handed over, and every edit marks the node it touched;
// printing sends the tree back with the original source, where any node
// still carrying its span prints from the original bytes. Nothing calls
// back into the host until then.
//
// Where Go returns nil, this returns null. A chain through a miss is
// `?.` rather than the silent no-op Go gets — TypeScript then makes the
// check compulsory, which is the better half of the trade.
const terraformClassesJS = `
(function () {
  // Captured now, not read at call time: the lockdown step deletes the
  // raw bindings once the host namespace has closed over what it needs,
  // and these have to keep working after that.
  var nativeTree = globalThis.__veilTfTree;
  var nativePrint = globalThis.__veilTfPrint;

  function labelAt(node, i) {
    return node && node.labels && node.labels.length > i ? node.labels[i] : '';
  }
  function setLabelAt(node, i, v) {
    node.labels = node.labels || [];
    while (node.labels.length <= i) node.labels.push('');
    node.labels[i] = String(v);
    node.changed = true;
  }

  // An item's identity is the node object itself, so removing means
  // finding it in the parent's array.
  function removeFrom(parentItems, node) {
    for (var i = 0; i < parentItems.length; i++) {
      if (parentItems[i] === node) {
        parentItems.splice(i, 1);
        return true;
      }
    }
    return false;
  }

  class TFAttribute {
    constructor(node, owner) { this.node = node; this.owner = owner; }
    name() { return this.node.name; }
    expr() { return this.node.expr; }
    setExpr(expr) { this.node.expr = String(expr); this.node.changed = true; return this; }
    delete() { return this.owner._removeNode(this.node); }
  }

  class TFComment {
    constructor(node, owner) { this.node = node; this.owner = owner; }
    text() { return this.node.text; }
    setText(t) { this.node.text = String(t); this.node.spanned = false; return this; }
    delete() { return this.owner._removeNode(this.node); }
  }

  // TFBlock is what every block type shares. The subclasses below add
  // only the labels their own block actually has — Terraform gives them
  // no uniform shape, so neither does this.
  class TFBlock {
    constructor(node, owner) { this.node = node; this.owner = owner; }
    blockType() { return this.node.type; }
    labels() { return (this.node.labels || []).slice(); }
    body() { return new TFBody(this.node, this.node.items || (this.node.items = [])); }
    delete() { return this.owner._removeNode(this.node); }

    // A block is the thing you looked up, so it answers about its own
    // contents directly rather than routing through body() each time.
    // body() is still there for holding onto one.
    attribute(name) { return this.body().attribute(name); }
    attributes() { return this.body().attributes(); }
    setAttribute(name, expr) { return this.body().setAttribute(name, expr); }
    removeAttribute(name) { return this.body().removeAttribute(name); }
    blocks() { return this.body().blocks(); }
    block(type) { return this.body().block.apply(this.body(), arguments); }
    addBlock(type) { return this.body().addBlock.apply(this.body(), arguments); }
    comments() { return this.body().comments(); }
    appendComment(text) { return this.body().appendComment(text); }
    items() { return this.body().items; }
    remove(item) { return this.body().remove(item); }
  }

  class TFResource extends TFBlock {
    resourceType() { return labelAt(this.node, 0); }
    setResourceType(t) { setLabelAt(this.node, 0, t); return this; }
    name() { return labelAt(this.node, 1); }
    setName(n) { setLabelAt(this.node, 1, n); return this; }
  }
  class TFDataSource extends TFBlock {
    dataType() { return labelAt(this.node, 0); }
    setDataType(t) { setLabelAt(this.node, 0, t); return this; }
    name() { return labelAt(this.node, 1); }
    setName(n) { setLabelAt(this.node, 1, n); return this; }
  }
  class TFEphemeral extends TFBlock {
    ephemeralType() { return labelAt(this.node, 0); }
    setEphemeralType(t) { setLabelAt(this.node, 0, t); return this; }
    name() { return labelAt(this.node, 1); }
    setName(n) { setLabelAt(this.node, 1, n); return this; }
  }
  class TFAction extends TFBlock {
    actionType() { return labelAt(this.node, 0); }
    setActionType(t) { setLabelAt(this.node, 0, t); return this; }
    name() { return labelAt(this.node, 1); }
    setName(n) { setLabelAt(this.node, 1, n); return this; }
  }
  class TFProvider extends TFBlock {
    name() { return labelAt(this.node, 0); }
    setName(n) { setLabelAt(this.node, 0, n); return this; }
    alias() {
      var a = this.body().attribute('alias');
      return a ? unquote(a.expr()) : '';
    }
  }
  class TFVariable extends TFBlock {
    name() { return labelAt(this.node, 0); }
    setName(n) { setLabelAt(this.node, 0, n); return this; }
  }
  class TFOutput extends TFBlock {
    name() { return labelAt(this.node, 0); }
    setName(n) { setLabelAt(this.node, 0, n); return this; }
  }
  class TFModule extends TFBlock {
    name() { return labelAt(this.node, 0); }
    setName(n) { setLabelAt(this.node, 0, n); return this; }
    source() {
      var a = this.body().attribute('source');
      return a ? unquote(a.expr()) : '';
    }
  }
  class TFCheck extends TFBlock {
    name() { return labelAt(this.node, 0); }
    setName(n) { setLabelAt(this.node, 0, n); return this; }
  }
  class TFTerraform extends TFBlock {}
  class TFLocals extends TFBlock {}
  class TFMoved extends TFBlock {}
  class TFRemoved extends TFBlock {}
  class TFImport extends TFBlock {}
  // A block this layer does not model: nested ones, or a type Terraform
  // adds later. Still readable and editable, just without named labels.
  class TFGeneric extends TFBlock {
    setLabels(labels) {
      this.node.labels = labels.map(String);
      this.node.changed = true;
      return this;
    }
  }

  var BLOCK_CLASSES = {
    resource: TFResource, data: TFDataSource, ephemeral: TFEphemeral, action: TFAction,
    provider: TFProvider, variable: TFVariable, output: TFOutput, module: TFModule,
    check: TFCheck, terraform: TFTerraform, locals: TFLocals, moved: TFMoved,
    removed: TFRemoved, import: TFImport
  };

  function wrapBlock(node, owner) {
    var Cls = BLOCK_CLASSES[node.type] || TFGeneric;
    return new Cls(node, owner);
  }

  function unquote(expr) {
    if (typeof expr === 'string' && expr.length >= 2 &&
        expr[0] === '"' && expr[expr.length - 1] === '"') {
      return expr.slice(1, -1);
    }
    return expr;
  }

  // TFBody is an ordered list of items — attributes, blocks and the
  // comments between them. owner is the node holding the list, marked
  // dirty when an item is added or removed so the host reprints it.
  class TFBody {
    constructor(ownerNode, items) { this.ownerNode = ownerNode; this.items = items; }

    // A block node records a change as "changed"; the file root records
    // it as "dirty". Different names because they are different messages
    // on the Go side, and the root's is what tells the printer a
    // generated file needs its own trailing newline.
    _touch() {
      if (!this.ownerNode) return;
      if (this.ownerNode.kind === 'block') this.ownerNode.changed = true;
      else this.ownerNode.dirty = true;
    }
    _removeNode(node) {
      var ok = removeFrom(this.items, node);
      if (ok) this._touch();
      return ok;
    }

    blocks() {
      var out = [], self = this;
      this.items.forEach(function (n) { if (n.kind === 'block') out.push(wrapBlock(n, self)); });
      return out;
    }
    attributes() {
      var out = [], self = this;
      this.items.forEach(function (n) { if (n.kind === 'attribute') out.push(new TFAttribute(n, self)); });
      return out;
    }
    comments() {
      var out = [], self = this;
      this.items.forEach(function (n) { if (n.kind === 'comment') out.push(new TFComment(n, self)); });
      return out;
    }
    attribute(name) {
      for (var i = 0; i < this.items.length; i++) {
        var n = this.items[i];
        if (n.kind === 'attribute' && n.name === name) return new TFAttribute(n, this);
      }
      return null;
    }
    setAttribute(name, expr) {
      var existing = this.attribute(name);
      if (existing) return existing.setExpr(expr);
      var node = { kind: 'attribute', name: name, expr: String(expr), spanned: false, changed: true };
      this.items.push(node);
      this._touch();
      return new TFAttribute(node, this);
    }
    removeAttribute(name) {
      var a = this.attribute(name);
      return a ? a.delete() : false;
    }
    appendComment(text) {
      var node = { kind: 'comment', text: String(text), spanned: false, changed: true };
      this.items.push(node);
      this._touch();
      return new TFComment(node, this);
    }
    remove(item) { return item ? item.delete() : false; }

    // _find and _add are what the typed accessors on both TFBody and
    // TFFile are built from.
    _find(type, labels) {
      for (var i = 0; i < this.items.length; i++) {
        var n = this.items[i];
        if (n.kind !== 'block' || n.type !== type) continue;
        var have = n.labels || [];
        if (labels.length > have.length) continue;
        var match = true;
        for (var j = 0; j < labels.length; j++) {
          if (labels[j] !== '' && have[j] !== labels[j]) { match = false; break; }
        }
        if (match) return wrapBlock(n, this);
      }
      return null;
    }
    _all(type, labels) {
      var out = [], self = this;
      this.items.forEach(function (n) {
        if (n.kind !== 'block' || n.type !== type) return;
        var have = n.labels || [];
        for (var j = 0; j < labels.length; j++) {
          if (labels[j] !== '' && have[j] !== labels[j]) return;
        }
        out.push(wrapBlock(n, self));
      });
      return out;
    }
    _add(type, labels) {
      var node = { kind: 'block', type: type, labels: labels, items: [], spanned: false, changed: true };
      this.items.push(node);
      this._touch();
      return wrapBlock(node, this);
    }
    // addBlock adds a block whose shape belongs to its parent rather
    // than to the file — lifecycle, connection, validation, provisioner.
    addBlock(type) {
      var labels = Array.prototype.slice.call(arguments, 1).map(String);
      return this._add(type, labels);
    }
    // block finds one of those, or null. Top-level blocks have their own
    // typed accessors on TFFile.
    block(type) {
      var labels = Array.prototype.slice.call(arguments, 1).map(String);
      return this._find(type, labels);
    }
  }

  // TFFile is the root. Its typed accessors take exactly the labels each
  // block has: two for a resource, one for a provider, none for locals.
  class TFFile {
    constructor(src, tree) {
      this.src = src;
      this.tree = tree;
      this._body = new TFBody(tree, tree.items || (tree.items = []));
    }
    body() { return this._body; }
    blocks() { return this._body.blocks(); }
    comments() { return this._body.comments(); }
    items() { return this._body.items; }
    remove(item) { return this._body.remove(item); }

    resource(t, n) { return this._body._find('resource', [t, n]); }
    resources(t) { return this._body._all('resource', t ? [t] : []); }
    addResource(t, n) { return this._body._add('resource', [String(t), String(n)]); }

    dataSource(t, n) { return this._body._find('data', [t, n]); }
    dataSources(t) { return this._body._all('data', t ? [t] : []); }
    addDataSource(t, n) { return this._body._add('data', [String(t), String(n)]); }

    ephemeral(t, n) { return this._body._find('ephemeral', [t, n]); }
    addEphemeral(t, n) { return this._body._add('ephemeral', [String(t), String(n)]); }

    action(t, n) { return this._body._find('action', [t, n]); }
    addAction(t, n) { return this._body._add('action', [String(t), String(n)]); }

    provider(n) { return this._body._find('provider', [n]); }
    providers() { return this._body._all('provider', []); }
    addProvider(n) { return this._body._add('provider', [String(n)]); }

    variable(n) { return this._body._find('variable', [n]); }
    variables() { return this._body._all('variable', []); }
    addVariable(n) { return this._body._add('variable', [String(n)]); }

    output(n) { return this._body._find('output', [n]); }
    outputs() { return this._body._all('output', []); }
    addOutput(n) { return this._body._add('output', [String(n)]); }

    module(n) { return this._body._find('module', [n]); }
    modules() { return this._body._all('module', []); }
    addModule(n) { return this._body._add('module', [String(n)]); }

    check(n) { return this._body._find('check', [n]); }
    checks() { return this._body._all('check', []); }
    addCheck(n) { return this._body._add('check', [String(n)]); }

    terraform() { return this._body._find('terraform', []); }
    addTerraform() { return this._body._add('terraform', []); }

    locals() { return this._body._all('locals', []); }
    addLocals() { return this._body._add('locals', []); }

    moved() { return this._body._all('moved', []); }
    addMoved() { return this._body._add('moved', []); }

    removed() { return this._body._all('removed', []); }
    addRemoved() { return this._body._add('removed', []); }

    imports() { return this._body._all('import', []); }
    addImport() { return this._body._add('import', []); }

    // toString prints through the host, which still holds the original
    // bytes: every node that kept its span comes back verbatim.
    toString() {
      return nativePrint(this.src, JSON.stringify(this.tree));
    }
  }

  globalThis.__veilTF = {
    parse: function (src) {
      var text = String(src == null ? '' : src);
      return new TFFile(text, JSON.parse(nativeTree(text)));
    },
    isFile: function (v) { return v instanceof TFFile; }
  };
})();
`
