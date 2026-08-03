import { closeConfirm, confirmDialog, confirmStore, useStore } from '../store';
import { openPath } from '../api';
import { riskyPath } from '../utils';

export default function ConfirmModal() {
  const c = useStore(confirmStore, (s) => s);
  if (!c.open) return null;
  return (
    <div className="modal-overlay">
      <div className="modal">
        <div className="modal-head">
          <span className="modal-title">{c.title}</span>
          <button className="btn x" onClick={closeConfirm}>
            ✕
          </button>
        </div>
        <div className="modal-body">
          <p className="confirm-text">{c.text}</p>
        </div>
        <div className="modal-foot">
          <span className="spacer" />
          <button className="btn ghost" onClick={closeConfirm}>
            Cancel
          </button>
          <button
            className="btn"
            onClick={() => {
              const fn = c.onYes;
              closeConfirm();
              fn?.();
            }}
          >
            {c.yesLabel || 'Open anyway'}
          </button>
        </div>
      </div>
    </div>
  );
}

// Open a path, asking first when the extension can execute code. The
// original single-file UI had this wired but never called it; History's
// Open button uses it so the README's "risky file" safety promise holds.
export function openPathWithWarning(path: string) {
  const doOpen = () => {
    openPath(path).catch(() => {});
  };
  if (riskyPath(path)) {
    confirmDialog(
      'Open risky file?',
      path + '\n\nThis file type can run code on your machine. Only open it if you trust where it came from.',
      'Open anyway',
      doOpen,
    );
  } else {
    doOpen();
  }
}
