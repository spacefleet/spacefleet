import { useLayoutEffect, useRef, useState } from "react";
import type {
  ChangeEvent,
  KeyboardEvent as ReactKeyboardEvent,
  SyntheticEvent,
} from "react";
import {
  applySuggestion,
  findActiveRef,
  suggestionsFor,
  type RefContext,
  type Suggestion,
} from "./refAutocomplete";

type FieldEl = HTMLInputElement | HTMLTextAreaElement;

interface Props {
  // Single-line (release name, namespace) vs multi-line (values.yaml).
  as: "input" | "textarea";
  value: string;
  onChange: (next: string) => void;
  // The known references this field may complete (vars, components, outputs).
  context: RefContext;
  disabled?: boolean;
  className?: string;
  placeholder?: string;
}

// nextSelectable returns the next non-disabled index in dir (+1/-1), wrapping —
// so arrow keys skip informational hint rows.
function nextSelectable(list: Suggestion[], from: number, dir: number): number {
  const n = list.length;
  if (n === 0) return from;
  let i = from;
  for (let step = 0; step < n; step++) {
    i = (i + dir + n) % n;
    if (!list[i].disabled) return i;
  }
  return from;
}

function firstSelectable(list: Suggestion[]): number {
  const i = list.findIndex((s) => !s.disabled);
  return i < 0 ? 0 : i;
}

// RefAutocompleteField wraps a text input or textarea with an inline `${{ }}`
// reference autocomplete: as the caret enters an open reference it offers the
// matching namespace / variable / component / output key (anchored just below
// the field — no caret-pixel math). Keyboard: ↑/↓ to move, Enter/Tab to accept,
// Esc to dismiss; a mouse click works too. The parser/suggester live in
// refAutocomplete.ts; this is the thin view.
export function RefAutocompleteField({
  as,
  value,
  onChange,
  context,
  disabled = false,
  className,
  placeholder,
}: Props) {
  const elRef = useRef<FieldEl | null>(null);
  const [suggestions, setSuggestions] = useState<Suggestion[]>([]);
  const [start, setStart] = useState(0);
  const [caret, setCaret] = useState(0);
  const [active, setActive] = useState(0);
  const pendingCaret = useRef<number | null>(null);

  function recompute(text: string, pos: number) {
    const ref = disabled ? null : findActiveRef(text, pos);
    if (!ref) {
      setSuggestions([]);
      return;
    }
    const next = suggestionsFor(ref.inner, context);
    setSuggestions(next);
    setStart(ref.start);
    setCaret(pos);
    setActive(firstSelectable(next));
  }

  function onInput(e: ChangeEvent<FieldEl>) {
    const el = e.target;
    onChange(el.value);
    recompute(el.value, el.selectionStart ?? el.value.length);
  }

  // Track caret moves (click / arrow / selection) so the popup follows the
  // caret in and out of a reference without an edit.
  function syncCaret(e: SyntheticEvent<FieldEl>) {
    const el = e.currentTarget;
    recompute(el.value, el.selectionStart ?? el.value.length);
  }

  function choose(s: Suggestion) {
    if (s.disabled) return;
    const res = applySuggestion(value, start, caret, s);
    onChange(res.text);
    pendingCaret.current = res.caret;
    // Recompute against the just-applied text so the next stage (e.g. variable
    // names after `vars.`) opens immediately.
    recompute(res.text, res.caret);
  }

  // After an accept rewrites the value, restore the caret into the (re-rendered)
  // controlled field.
  useLayoutEffect(() => {
    if (pendingCaret.current != null && elRef.current) {
      const c = pendingCaret.current;
      pendingCaret.current = null;
      elRef.current.focus();
      elRef.current.setSelectionRange(c, c);
    }
  });

  function onKeyDown(e: ReactKeyboardEvent<FieldEl>) {
    if (suggestions.length === 0) return;
    if (e.key === "Escape") {
      e.preventDefault();
      setSuggestions([]);
      return;
    }
    const hasSelectable = suggestions.some((s) => !s.disabled);
    if (!hasSelectable) return;
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActive((i) => nextSelectable(suggestions, i, 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActive((i) => nextSelectable(suggestions, i, -1));
    } else if (e.key === "Enter" || e.key === "Tab") {
      const s = suggestions[active];
      if (s && !s.disabled) {
        e.preventDefault();
        choose(s);
      }
    }
  }

  const shared = {
    ref: elRef as React.Ref<FieldEl & HTMLInputElement & HTMLTextAreaElement>,
    value,
    className,
    placeholder,
    disabled,
    onChange: onInput,
    onKeyDown,
    onClick: syncCaret,
    onKeyUp: syncCaret,
    onSelect: syncCaret,
    // Close on blur; a suggestion click uses onMouseDown (preventDefault) so it
    // fires before blur and keeps focus.
    onBlur: () => setSuggestions([]),
  };

  return (
    <div className="relative">
      {as === "textarea" ? (
        <textarea {...shared} />
      ) : (
        <input type="text" {...shared} />
      )}
      {suggestions.length > 0 && (
        <ul className="absolute left-0 right-0 top-full z-20 mt-0.5 max-h-56 overflow-auto border border-neutral-300 bg-white shadow-md">
          {suggestions.map((s, i) => (
            <li key={s.label + ":" + i}>
              <button
                type="button"
                disabled={s.disabled}
                onMouseDown={(e) => {
                  e.preventDefault();
                  choose(s);
                }}
                className={
                  "flex w-full items-center justify-between gap-3 px-2 py-1 text-left " +
                  (s.disabled
                    ? "cursor-default text-neutral-400"
                    : i === active
                      ? "bg-neutral-100 text-neutral-900"
                      : "text-neutral-700 hover:bg-neutral-50")
                }
              >
                <span className="font-mono text-xs">{s.label}</span>
                {s.detail && (
                  <span className="shrink-0 text-[11px] text-neutral-400">
                    {s.detail}
                  </span>
                )}
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
