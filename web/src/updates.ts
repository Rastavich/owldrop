// Update actions shared by the update popup and Settings. Desktop release
// builds replace the binary in place and restart; everything else (server
// builds, Docker/Nix images, read-only MSIX/Store installs) has to fetch the
// release by hand.
import { installUpdate } from './api';
import { toast } from './store';

// Public download page — the same one the website's download buttons use.
export const DOWNLOAD_URL = 'https://owldrop.app/#downloads';

// installInPlace downloads the pending release and restarts into it. A started
// install reports itself through the updater's own events (installed / error);
// only a failure to start lands here as a toast. Returns false when the
// install did not start — success ends with the app restarting, so callers can
// leave their busy state set.
export async function installInPlace(): Promise<boolean> {
  try {
    await installUpdate();
    return true;
  } catch (e) {
    toast('Update failed: ' + (e instanceof Error ? e.message : e), undefined, 'err');
    return false;
  }
}
