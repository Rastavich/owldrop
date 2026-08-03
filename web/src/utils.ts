// Pure helpers ported from the original single-file UI.
import { toast } from './store';

export function fmtSize(n: number): string {
  if (n < 0) return '?';
  if (n < 1024) return n + ' B';
  const u = ['KiB', 'MiB', 'GiB', 'TiB'];
  let v = n;
  let i = -1;
  do {
    v /= 1024;
    i++;
  } while (v >= 1024 && i < u.length - 1);
  return (v >= 100 ? v.toFixed(0) : v.toFixed(1)) + ' ' + u[i];
}

export function fmtAge(t: string): string {
  const s = (Date.now() - new Date(t).getTime()) / 1000;
  if (s < 10) return 'just now';
  if (s < 60) return Math.round(s) + 's ago';
  if (s < 3600) return Math.round(s / 60) + 'm ago';
  if (s < 86400) return Math.round(s / 3600) + 'h ago';
  return Math.round(s / 86400) + 'd ago';
}

const IMG = ['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'heic', 'bmp', 'avif', 'raw'];
const VID = ['mp4', 'mkv', 'mov', 'avi', 'webm', 'm4v'];
const AUD = ['mp3', 'wav', 'flac', 'ogg', 'm4a', 'aac', 'opus'];
const ARC = ['zip', 'tar', 'gz', 'bz2', 'xz', '7z', 'rar', 'zst'];
const DOC = ['pdf', 'doc', 'docx', 'xls', 'xlsx', 'ppt', 'pptx', 'odt', 'ods', 'md', 'txt', 'rtf', 'csv'];
const CODE = ['go', 'rs', 'py', 'js', 'ts', 'c', 'h', 'cpp', 'java', 'sh', 'html', 'css', 'json', 'yaml', 'yml', 'toml'];

export type ChipClass = 'c-img' | 'c-vid' | 'c-aud' | 'c-arc' | 'c-doc' | 'c-code' | '';

export function chipClass(name: string): ChipClass {
  const ext = name.includes('.') ? name.split('.').pop()!.toLowerCase() : '';
  if (IMG.includes(ext)) return 'c-img';
  if (VID.includes(ext)) return 'c-vid';
  if (AUD.includes(ext)) return 'c-aud';
  if (ARC.includes(ext)) return 'c-arc';
  if (DOC.includes(ext)) return 'c-doc';
  if (CODE.includes(ext)) return 'c-code';
  return '';
}

export function chipLabel(name: string): string {
  const i = name.lastIndexOf('.');
  if (i <= 0 || i === name.length - 1) return 'FILE';
  const e = name.slice(i + 1).toUpperCase();
  return e.length > 5 ? e.slice(0, 4) + '…' : e;
}

export type FileType = 'img' | 'vid' | 'doc' | 'other';

export function fileType(name: string): FileType {
  const cls = chipClass(name);
  if (cls === 'c-img') return 'img';
  if (cls === 'c-vid') return 'vid';
  if (cls === 'c-aud' || cls === 'c-arc' || cls === 'c-doc' || cls === 'c-code') return 'doc';
  return 'other';
}

export function uuid(): string {
  return (
    crypto.randomUUID?.() ||
    'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, (c) => {
      const r = (Math.random() * 16) | 0;
      return (c === 'x' ? r : (r & 0x3) | 0x8).toString(16);
    })
  );
}

export function stamp(): string {
  const d = new Date();
  const p = (n: number) => String(n).padStart(2, '0');
  return (
    d.getFullYear() + p(d.getMonth() + 1) + p(d.getDate()) + '-' + p(d.getHours()) + p(d.getMinutes()) + p(d.getSeconds())
  );
}

export async function copyText(text: string, silent = false): Promise<boolean> {
  const fallback = () => {
    const ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    let did = false;
    try {
      did = document.execCommand('copy');
    } catch {
      /* noop */
    }
    ta.remove();
    return did;
  };
  let ok = false;
  if (navigator.clipboard && navigator.clipboard.writeText) {
    try {
      await navigator.clipboard.writeText(text);
      ok = true;
    } catch {
      ok = fallback();
    }
  } else {
    ok = fallback();
  }
  if (!silent) {
    toast(ok ? 'Copied to clipboard' : "Couldn't copy — select the URL and copy manually", undefined, ok ? 'ok' : 'err');
  }
  return ok;
}

const RISKY_EXT = ['exe', 'msi', 'bat', 'cmd', 'sh', 'appimage', 'deb', 'rpm', 'run', 'ps1', 'dll', 'scr', 'jar', 'bin', 'elf', 'com', 'cpl', 'gadget', 'ins', 'iso', 'js', 'jse', 'lnk', 'msc', 'msp', 'reg', 'vbs', 'vbe', 'wsf', 'wsh', 'py', 'php', 'pl', 'rb'];

export function riskyPath(p: string): boolean {
  const i = p.lastIndexOf('.');
  if (i <= 0 || i === p.length - 1) return false;
  return RISKY_EXT.includes(p.slice(i + 1).toLowerCase());
}

export function pct(w: number, s: number): number {
  return s > 0 ? Math.min(100, Math.round((w * 100) / s)) : 0;
}
