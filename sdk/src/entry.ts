// Bundle entry: expose the SDK as window.twillingate and auto-init in
// snippet mode (a data-key on the loading <script> tag).
import { Twillingate, autoInit, VERSION } from "./twillingate";

const tg = new Twillingate();

declare global {
  interface Window {
    twillingate: Twillingate & { VERSION: string };
  }
}

(tg as Twillingate & { VERSION: string }).VERSION = VERSION;
window.twillingate = tg as Twillingate & { VERSION: string };

autoInit(tg, document.currentScript as HTMLScriptElement | null);
