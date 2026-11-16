import { useId, useRef, useState, type ReactNode } from "react";
import { Icon, type IconName } from "../Icon";

export function ControlMenu({ label, icon, children, trigger, onOpenChange }: { label: string; icon: IconName; children: ReactNode; trigger?: ReactNode; onOpenChange?: (open: boolean) => void }) {
  const id = useId();
  const [open, setOpen] = useState(false);
  const button = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  function toggle() {
    const element = menu.current;
    const anchor = button.current;
    if (!element || !anchor) return;
    if (element.matches(":popover-open")) { element.hidePopover(); return; }
    const rect = anchor.getBoundingClientRect();
    const width = Math.min(360, window.innerWidth - 24);
    const top = Math.min(rect.bottom + 6, Math.max(12, window.innerHeight - 240));
    Object.assign(element.style, { left: `${Math.max(12, Math.min(rect.right - width, window.innerWidth - width - 12))}px`, top: `${top}px`, width: `${width}px`, maxHeight: `${window.innerHeight - top - 12}px` });
    element.showPopover();
  }
  return <>
    <button type="button" ref={button} className={trigger ? "wb-control-trigger" : "wb-icon-btn"} aria-label={label} title={label} aria-haspopup="dialog" aria-expanded={open} aria-controls={id} onClick={toggle}>{trigger ?? <Icon name={icon} size={16} />}</button>
    <div id={id} className="wb-control-menu" ref={menu} popover="auto" role="dialog" aria-label={label} onToggle={() => { const next = Boolean(menu.current?.matches(":popover-open")); setOpen(next); onOpenChange?.(next); }}>
      <header><strong>{label}</strong><button type="button" className="wb-icon-btn" aria-label={`关闭${label}`} onClick={() => menu.current?.hidePopover()}><Icon name="close" size={14} /></button></header>
      {children}
    </div>
  </>;
}
