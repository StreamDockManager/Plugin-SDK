// Node-compatible runtimes share UTF-8 accounting without allocating encoded
// arrays. Keep input, state, cache, network and diagnostic budgets consistent
export const byteLength = Buffer.byteLength;
