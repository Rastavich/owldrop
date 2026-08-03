import { daemonStore, useStore } from '../store';

export default function DaemonPill() {
  const s = useStore(daemonStore, (s) => s);
  const cls = s.ok === null ? '' : s.ok ? ' ok' : ' err';
  return (
    <div className={'pill' + cls}>
      <span className="dot" />
      <span>{s.msg}</span>
    </div>
  );
}
