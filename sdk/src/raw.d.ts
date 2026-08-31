// Vite (and therefore vitest) serves any module suffixed with ?raw as its
// source text. Declared here so the parity test can read twillingate.ts
// without pulling @types/node in for one assertion.
declare module "*?raw" {
  const content: string;
  export default content;
}
