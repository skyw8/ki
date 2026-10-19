export function Switch({ on, onChange, testid, ariaLabel, disabled }: { on: boolean; onChange: (on: boolean) => void; testid?: string; ariaLabel: string; disabled?: boolean }) {
  return (
    <button type="button" role="switch" aria-label={ariaLabel} aria-checked={on} className={`switch${on ? ' on' : ''}`} data-testid={testid} disabled={disabled} onClick={() => onChange(!on)}>
      <span className="switch-knob" aria-hidden />
    </button>
  )
}
