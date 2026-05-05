# Veil
Veil is a CLI for deployment configuration management. It is a generic engine that enables repository owners to define `kinds` that
exist within the repository. Those `kinds` are consumed downstream by developers who don't have the domain knowledge in provisioning
the underlying resources necessary for the `kind`.

# Fundamental concepts

## Kind
A `kind` is a blueprint for creating a `resource` (think class -> object). A `kind` consists of two fundamental pieces:

1. Sources: files that comprise the `kind`
2. Hooks: typescript code that manipulate the sources

Every `kind` is registered to the `veil.json` file at the root of the repository for example:

```
// veil.json
{
  "$schema": "https://raw.githubusercontent.com/vercel/veil/main/api/jsonschema/VeilConfigDefinition.schema.json"
  "kinds": [
    // The path to the definition file for the 'service' kind
    "./.veil/kinds/service/kind.json"
  ],
  "registries": {
    "": "./public/r/registry.json"
  }
}
```

### The `kind.json` file
You can create a new kind by running
```
veil new kind service
```
This will produce a `kind.json`
```
{
  "$schema": "https://raw.githubusercontent.com/vercel/veil/main/api/jsonschema/KindDefinition.schema.json",
  "name": "derp",
  "schema": "./schema.json",
  "hooks": {
    "render": [
      {
        "path": "./hooks/src/hello-world.ts"
      }
    ]
  },
  "sources": [
    "./sources/source.txt"
  ]
}
```
You'll notice that there are 3 primary fields:

1. `schema`: The JSON schema that defines what user's of this kind will supply you.
2. `hooks.render`: A list of files that are executed when `veil render` is run on this kind.
3. `sources`: A list of files that the `kind` comprises.

Within `hello-world.ts` you can see the following generated code:
```typescript
import type { FS, RenderHook, RenderHookContext } from './veil-types';

const helloWorld: RenderHook = {
  render(ctx: RenderHookContext, fs: FS): FS {
    // TODO: implement
    return fs;
  },
};

export default helloWorld;
```
This is just standard ts. You can manipulate the file in whatever way you want including changing the output path or deleting it from 
