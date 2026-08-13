import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { Link } from '@tanstack/react-router';
import { createDropLink, getPhoneAccess } from '../api';
import { copyText } from '../utils';
import { toast } from '../store';

export default function EmptyInbox() {
  const qc = useQueryClient();
  const { data: phone } = useQuery({ queryKey: ['phone'], queryFn: getPhoneAccess });
  const [busy, setBusy] = useState(false);

  const dropTest = async () => {
    setBusy(true);
    try {
      const res = await createDropLink('Try Owldrop', 60, 1, 0);
      const token = res.link.token;
      const fd = new FormData();
      fd.append(
        'file',
        new Blob(['Hello from Owldrop.\nThis file landed in your inbox.\n'], { type: 'text/plain' }),
        'owldrop-hello.txt',
      );
      const up = await fetch('/drop/' + token + '/upload', { method: 'POST', body: fd });
      if (!up.ok) {
        const err = await up.json().catch(() => ({}));
        throw new Error((err as { error?: string }).error || 'upload failed');
      }
      toast('Test file is in your inbox — Save or Delete it');
      qc.invalidateQueries({ queryKey: ['inbox'] });
      qc.invalidateQueries({ queryKey: ['droplinks'] });
    } catch (e) {
      toast("Couldn't drop a test file: " + (e instanceof Error ? e.message : e), undefined, 'err');
    }
    setBusy(false);
  };

  const mintLink = async () => {
    setBusy(true);
    try {
      const res = await createDropLink('Inbox', 60, 0, 4);
      const url = res.shareUrl || res.publicUrl || res.url;
      await copyText(url, true);
      toast(res.publicUrl ? 'Public drop link copied' : 'Drop link copied (tailnet only — enable Public access for anyone on the internet)');
      qc.invalidateQueries({ queryKey: ['droplinks'] });
    } catch (e) {
      toast("Couldn't create link: " + (e instanceof Error ? e.message : e), undefined, 'err');
    }
    setBusy(false);
  };

  const phoneUrl = phone?.url || '';

  return (
    <div className="empty empty-onboard">
      <p className="t">This is the drop box</p>
      <p className="s">
        Files sent over Taildrop, a drop link, or from your phone land here. Complete one transfer so you know the loop works.
      </p>
      <div className="onboard-actions">
        <button className="btn" disabled={busy} onClick={dropTest}>
          Drop a test file into this inbox
        </button>
        <button className="btn ghost" disabled={busy} onClick={mintLink}>
          Copy a drop link
        </button>
        <Link to="/sync" className="btn ghost">
          Paste a URL on Sync
        </Link>
      </div>
      {phoneUrl ? (
        <div className="phone-qr">
          {/* Same-origin PNG: CSP is img-src 'self' data: — blob: URLs render as a blank box. */}
          <img src={'/api/qr?u=' + encodeURIComponent(phoneUrl)} width={160} height={160} alt="QR code to open Owldrop on your phone" />
          <div>
            <p className="t">Open on your phone</p>
            <p className="s">On the same tailnet, scan or visit:</p>
            <code className="phone-url">{phoneUrl}</code>
            <button className="btn ghost mini" onClick={() => copyText(phoneUrl)}>
              Copy URL
            </button>
          </div>
        </div>
      ) : (
        <p className="s onboard-hint">
          To send from a phone browser, turn on <strong>LAN mode</strong> in{' '}
          <Link to="/settings">Settings</Link>. Public HTTPS on your <code>*.ts.net</code> name is drop links only — the
          QR uses your tailnet IP instead.
        </p>
      )}
    </div>
  );
}
