import { useEffect, useRef, useState } from "react";

// Unsaved work: the one page that holds some registers a check here, and
// everything that would throw that page away asks first.
let unsaved: () => boolean = () => false;

export const hasUnsaved = () => unsaved();

// mayLeave reports whether the current page may be discarded.
export const mayLeave = () => !unsaved() || confirm("Есть несохранённые изменения. Уйти без сохранения?");

// dropUnsaved forgets the check, for work that must be discarded without asking.
export const dropUnsaved = () => {
  unsaved = () => false;
};

// useUnsavedGuard protects the calling page while isDirty returns true: menu
// links and Back ask first, and so does closing or reloading the browser tab.
export function useUnsavedGuard(isDirty: () => boolean) {
  const latest = useRef(isDirty);
  latest.current = isDirty;
  useEffect(() => {
    unsaved = () => latest.current();
    const warn = (e: BeforeUnloadEvent) => {
      if (unsaved()) e.preventDefault();
    };
    addEventListener("beforeunload", warn);
    return () => {
      dropUnsaved();
      removeEventListener("beforeunload", warn);
    };
  }, []);
}

// position numbers the current history entry. An entry without a number is new:
// it was just pushed on top of the entry numbered `previous`.
function position(previous: number): number {
  const known = (history.state as { pos?: number } | null)?.pos;
  if (known !== undefined) return known;
  history.replaceState({ pos: previous + 1 }, "");
  return previous + 1;
}

// useHash returns the current location hash (without '#') and re-renders on change.
export function useHash(): string {
  const [h, setH] = useState(() => location.hash.slice(1) || "/");
  useEffect(() => {
    // The entry of the page now on screen; an unnumbered one is the newest.
    let here = position(history.length - 2);
    const on = () => {
      const there = position(here);
      // A hash change cannot be cancelled, so a refusal walks the history back
      // to where we were. Arriving there (there === here) asks nothing.
      if (there !== here && !mayLeave()) return history.go(here - there);
      here = there;
      setH(location.hash.slice(1) || "/");
    };
    addEventListener("hashchange", on);
    return () => removeEventListener("hashchange", on);
  }, []);
  return h;
}

export const go = (path: string) => {
  location.hash = path;
};
