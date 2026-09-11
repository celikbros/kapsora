// The one Vite value the ui package reads: the base path of the app it is built into, so a
// link to "home" lands on the app's own root under /portal/ or /uye/ as well as at /.
interface ImportMetaEnv {
  readonly BASE_URL: string;
}
interface ImportMeta {
  readonly env: ImportMetaEnv;
}
