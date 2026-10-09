import { useLayoutEffect, useRef } from "react";
import type { InputHTMLAttributes } from "react";
import { slugify } from "../lib/slug";

type SlugInputProps = Omit<
  InputHTMLAttributes<HTMLInputElement>,
  "value" | "onChange" | "type"
> & {
  value: string;
  onChange: (value: string) => void;
};

// SlugInput is a text input that only ever holds a slug: each keystroke or
// paste is corrected by slugify, keeping the caret where the user was typing
// (a controlled input whose value is rewritten would otherwise jump to the end).
export function SlugInput({ value, onChange, onBlur, ...rest }: SlugInputProps) {
  const ref = useRef<HTMLInputElement>(null);
  const caret = useRef<number | null>(null);

  useLayoutEffect(() => {
    if (caret.current === null || !ref.current) return;
    ref.current.setSelectionRange(caret.current, caret.current);
    caret.current = null;
  });

  return (
    <input
      {...rest}
      ref={ref}
      type="text"
      autoCapitalize="none"
      autoCorrect="off"
      spellCheck={false}
      // Correction leaves a trailing hyphen as the only way to miss the pattern.
      pattern="[a-z0-9]([-a-z0-9]*[a-z0-9])?"
      title="Can't end with a hyphen"
      maxLength={63}
      value={value}
      onChange={(e) => {
        const raw = e.target.value;
        const pos = e.target.selectionStart ?? raw.length;
        const next = slugify(raw);
        caret.current = slugify(raw.slice(0, pos)).length;
        // A rejected character leaves the value unchanged, so React won't
        // re-render to put it back — reset the DOM ourselves.
        if (next === value) {
          e.target.value = next;
          e.target.setSelectionRange(caret.current, caret.current);
          caret.current = null;
          return;
        }
        onChange(next);
      }}
      onBlur={(e) => {
        const trimmed = value.replace(/-+$/, "");
        if (trimmed !== value) onChange(trimmed);
        onBlur?.(e);
      }}
    />
  );
}
