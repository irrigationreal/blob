# Functions

`form: function` deploys a small HTTP handler without asking you to write an HTTP server or Dockerfile. Blob builds a generated Node.js wrapper image, schedules it like a web service, and publishes the usual HTTPS route.

```yaml
name: hello-fn
form: function
handler: index.mjs
runtime: nodejs
```

You can also deploy directly from a folder:

```sh
blob deploy --function --name hello-fn --handler index.mjs
```

If `handler` is omitted, Blob looks for `index.mjs`, `index.js`, `function.mjs`, `function.js`, `handler.mjs`, then `handler.js` under `root`.

## Handler contract

The module must export `default` or `handler`:

```js
export default async function handler(event) {
  return {
    statusCode: 200,
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ ok: true, path: event.path }),
  };
}
```

`event` contains:

| field | value |
|---|---|
| `method` | HTTP method |
| `path` | URL path |
| `query` | query parameters as an object |
| `headers` | request headers |
| `body` | UTF-8 body string or `null` |
| `rawBody` | base64 body |
| `isBase64Encoded` | always `false` for now |

Return shapes:

| return value | response |
|---|---|
| `undefined` | 204 |
| string or Buffer | raw body |
| plain object | JSON body |
| `{statusCode, headers, body}` | explicit response |
| Web `Response` | status, headers, body copied |

## Build and root

`root` points at the function source directory. `build` runs before the handler is resolved, so build output can become the function root.

```yaml
name: built-fn
form: function
build: npm run build
root: dist
handler: index.mjs
```

If `package.json` exists in the function root, the generated image runs `npm ci --omit=dev` when a lockfile exists, otherwise `npm install --omit=dev`.

## Resources

Functions default to 100 CPU shares and 128 MiB RAM. Override with `cpu:` and `memory:` if dependencies need more room.

## Limits

This is an HTTP function path. Blob does not yet provide event triggers, per-request scale-to-zero, queues, or language runtimes beyond Node.js. Use `blob jobs run` / `blob jobs schedule` for batch work.
