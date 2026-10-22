import React from 'react';
import paths from '../../../docs/_data/icons.json';

// 图标路径与文档网站共用（docs/_data/icons.json）。
export function Icon({ name, size = 18, ...props }) {
  return <svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor"
    strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false" {...props}>
    <path d={paths[name] || paths.file} />
  </svg>;
}

export function Logo({ size = 24 }) {
  return <svg width={size} height={size} viewBox="0 0 32 32" aria-hidden="true" focusable="false">
    <defs>
      <linearGradient id="psdkit-logo" x1="4" y1="4" x2="28" y2="16" gradientUnits="userSpaceOnUse">
        <stop offset="0" stopColor="#22e4ff" /><stop offset=".55" stopColor="#4f7bff" /><stop offset="1" stopColor="#8b7bff" />
      </linearGradient>
    </defs>
    <path d="M4 20.5 16 26.5l12-6" fill="none" stroke="#8b7bff" strokeOpacity=".5" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
    <path d="M4 15.5 16 21.5l12-6" fill="none" stroke="#4f7bff" strokeOpacity=".85" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
    <path d="M16 4.5 28 10.5 16 16.5 4 10.5 16 4.5Z" fill="url(#psdkit-logo)" />
  </svg>;
}
