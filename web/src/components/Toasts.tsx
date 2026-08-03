import { dismissToast, toastsStore, useStore } from '../store';

export default function Toasts() {
  const toasts = useStore(toastsStore, (s) => s);
  return (
    <div className="toasts">
      {toasts.map((t) => (
        <div key={t.id} className={'toast ' + t.kind + (t.out ? ' out' : '')}>
          <span>{t.msg}</span>
          {t.actions?.map((a) => (
            <button
              key={a.label}
              className="toast-act"
              onClick={() => {
                a.fn();
                dismissToast(t.id);
              }}
            >
              {a.label}
            </button>
          ))}
        </div>
      ))}
    </div>
  );
}
