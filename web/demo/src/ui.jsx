import React, { useEffect, useId, useRef, useState } from 'react';
import { Icon } from './icons.jsx';

// 下拉菜单：方向键移动、Esc 关闭并归还焦点、点击外部关闭。
export function Menu({ label, icon, items, disabled, align = 'left', className = '', primary = false }) {
  const [open, setOpen] = useState(false);
  const root = useRef(null);
  const button = useRef(null);
  const id = useId();
  useEffect(() => {
    if (!open) return undefined;
    const close = (event) => { if (!root.current?.contains(event.target)) setOpen(false); };
    document.addEventListener('pointerdown', close);
    root.current?.querySelector('[role="menuitem"]:not(:disabled)')?.focus();
    return () => document.removeEventListener('pointerdown', close);
  }, [open]);
  function onKeyDown(event) {
    const entries = [...root.current.querySelectorAll('[role="menuitem"]:not(:disabled)')];
    const index = entries.indexOf(document.activeElement);
    if (event.key === 'Escape') { setOpen(false); button.current?.focus(); }
    else if (event.key === 'ArrowDown') { event.preventDefault(); entries[(index + 1) % entries.length]?.focus(); }
    else if (event.key === 'ArrowUp') { event.preventDefault(); entries[(index - 1 + entries.length) % entries.length]?.focus(); }
    else if (event.key === 'Tab') setOpen(false);
  }
  return <div className={`menu ${className}`} ref={root} onKeyDown={onKeyDown}>
    <button ref={button} type="button" className={`tb-btn ${primary ? 'tb-btn--primary' : ''}`} aria-haspopup="menu" aria-expanded={open}
      aria-controls={id} disabled={disabled} onClick={() => setOpen(!open)}>
      {icon ? <Icon name={icon} size={16} /> : null}<span className="tb-btn__label">{label}</span><Icon name="chevron" size={14} className="tb-btn__caret" />
    </button>
    {open ? <div className={`menu__pop menu__pop--${align}`} role="menu" id={id} aria-label={label}>
      {items.filter(Boolean).map((item, index) => item === '-' ? <hr key={`sep-${index}`} /> :
        <button key={item.label} type="button" role="menuitem" disabled={item.disabled} className={item.danger ? 'is-danger' : ''}
          onClick={() => { setOpen(false); item.onSelect(); }}>
          {item.icon ? <Icon name={item.icon} size={16} /> : <span className="menu__spacer" />}
          <span className="menu__text"><span>{item.label}</span>{item.hint ? <small>{item.hint}</small> : null}</span>
          {item.shortcut ? <kbd>{item.shortcut}</kbd> : null}
        </button>)}
    </div> : null}
  </div>;
}

// 模态对话框：打开时聚焦首个控件，Tab 在内部循环，Esc 取消。
export function Dialog({ title, children, actions, onClose, tone }) {
  const panel = useRef(null);
  const titleId = useId();
  useEffect(() => {
    const previous = document.activeElement;
    const focusable = panel.current?.querySelectorAll('input, select, textarea, button');
    (focusable?.[0] || panel.current)?.focus();
    return () => previous?.focus?.();
  }, []);
  function onKeyDown(event) {
    if (event.key === 'Escape') { event.stopPropagation(); onClose(); }
    if (event.key !== 'Tab') return;
    const focusable = [...panel.current.querySelectorAll('input, select, textarea, button:not(:disabled)')];
    if (!focusable.length) return;
    const first = focusable[0], last = focusable.at(-1);
    if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
    else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
  }
  return <div className="dialog" onKeyDown={onKeyDown}>
    <div className="dialog__backdrop" onClick={onClose} />
    <div className={`dialog__panel ${tone ? `dialog__panel--${tone}` : ''}`} role="dialog" aria-modal="true" aria-labelledby={titleId} ref={panel} tabIndex={-1}>
      <h2 id={titleId}>{tone === 'danger' ? <Icon name="warning" size={18} /> : null}{title}</h2>
      <div className="dialog__body">{children}</div>
      <div className="dialog__actions">{actions}</div>
    </div>
  </div>;
}

// 选项卡：左右方向键切换，符合 WAI-ARIA tabs 模式。
export function Tabs({ tabs, value, onChange, label, className = '', trailing }) {
  const list = useRef(null);
  function onKeyDown(event) {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const index = tabs.findIndex((tab) => tab.id === value);
    const next = event.key === 'Home' ? 0 : event.key === 'End' ? tabs.length - 1
      : (index + (event.key === 'ArrowRight' ? 1 : -1) + tabs.length) % tabs.length;
    onChange(tabs[next].id);
    list.current?.querySelectorAll('[role="tab"]')[next]?.focus();
  }
  return <div className={`tabs ${className}`} role="tablist" aria-label={label} ref={list} onKeyDown={onKeyDown}>
    {tabs.map((tab) => <button key={tab.id} type="button" role="tab" id={`tab-${tab.id}`} aria-selected={value === tab.id}
      aria-controls={`panel-${tab.id}`} tabIndex={value === tab.id ? 0 : -1} onClick={() => onChange(tab.id)}>
      {tab.icon ? <Icon name={tab.icon} size={15} /> : null}{tab.label}{tab.badge != null ? <span className="tabs__badge">{tab.badge}</span> : null}
    </button>)}
    {trailing}
  </div>;
}

export function Meter({ label, used, limit, format = (value) => value }) {
  const ratio = limit ? Math.min(1, used / limit) : 0;
  const tone = ratio > .9 ? 'danger' : ratio > .7 ? 'warn' : 'ok';
  return <div className={`meter meter--${tone}`}>
    <div className="meter__row"><span>{label}</span><span>{format(used)} / {format(limit)}</span></div>
    <div className="meter__bar" role="meter" aria-label={label} aria-valuemin={0} aria-valuemax={limit} aria-valuenow={used}
      aria-valuetext={`${format(used)} / ${format(limit)}`}><i style={{ width: `${ratio * 100}%` }} /></div>
  </div>;
}

export function Elapsed({ since }) {
  const [now, setNow] = useState(() => performance.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(performance.now()), 200);
    return () => clearInterval(timer);
  }, [since]);
  return <span className="elapsed">{((now - since) / 1000).toFixed(1)} s</span>;
}
