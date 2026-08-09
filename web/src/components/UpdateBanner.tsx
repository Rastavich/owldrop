import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { getUpdateState } from '../api';

export default function UpdateBanner() {
  const { data: update } = useQuery({ queryKey: ['update'], queryFn: getUpdateState });

  if (!update?.available) return null;

  return (
    <div className="update-banner">
      <span>
        Version <strong>{update.latest}</strong> is available (you're on {update.current}).
      </span>
      <Link to="/settings" className="btn mini">
        Open Settings
      </Link>
    </div>
  );
}
