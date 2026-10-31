import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { getUpdateState } from '../api';
import { confirmStore, pickerStore, useStore } from '../store';
import { DOWNLOAD_URL, installInPlace } from '../updates';

// UpdatePopup is the one interruption that tells the user a newer release
// exists and lets them install it without opening Settings. It asks once per
// start: "Later" only hides it until the next launch, where it comes back (and
// keeps coming back) until the app is updated.
export default function UpdatePopup() {
  const { data: update } = useQuery({ queryKey: ['update'], queryFn: getUpdateState });
  const [dismissed, setDismissed] = useState('');
  const [busy, setBusy] = useState(false);
  // Another dialog is already up (saving a file, confirming something) — wait
  // rather than stack a second overlay on top of it.
  const confirmOpen = useStore(confirmStore, (s) => s.open);
  const pickerOpen = useStore(pickerStore, (s) => s.open);

  const latest = update?.latest ?? '';
  const hidden = !update?.available || !latest || latest === dismissed || confirmOpen || pickerOpen;
  if (hidden) return null;

  const downloading = update.state === 'downloading' || update.state === 'installed';
  const actionLabel =
    update.state === 'installed'
      ? 'Restarting…'
      : update.state === 'downloading'
        ? 'Downloading…'
        : busy
          ? 'Starting…'
          : 'Update now';

  const later = () => setDismissed(latest);

  const install = async () => {
    setBusy(true);
    // Success restarts the app, so only a failure re-enables the button.
    if (!(await installInPlace())) setBusy(false);
  };

  return (
    <div className="modal-overlay">
      <div className="modal update-popup">
        <div className="modal-head">
          <span className="modal-title">New version available</span>
          <button className="btn x" onClick={later} disabled={downloading} aria-label="Close">
            ✕
          </button>
        </div>
        <div className="modal-body">
          <p className="update-lead">
            Owldrop <strong>{latest}</strong> is out — you're running {update.current}.
          </p>
          <p className="update-hint">
            {downloading
              ? 'Downloading the update — the app restarts into it when it is ready.'
              : update.autoInstall
                ? 'Owldrop replaces itself in place and restarts. Files already in your inbox are kept.'
                : 'This build does not update itself. Download the release for your platform and install it over this one.'}
          </p>
        </div>
        <div className="modal-foot">
          <span className="spacer" />
          <button className="btn ghost" onClick={later} disabled={downloading}>
            Later
          </button>
          {update.autoInstall ? (
            <button className="btn" onClick={install} disabled={busy || downloading}>
              {actionLabel}
            </button>
          ) : (
            <a className="btn" href={DOWNLOAD_URL} target="_blank" rel="noopener">
              Download the latest version
            </a>
          )}
        </div>
      </div>
    </div>
  );
}
