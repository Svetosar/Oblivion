import { useEffect, useState } from "react";

// useHash returns the current location hash (without '#') and re-renders on change.
export function useHash(): string {
  const [h, setH] = useState(() => location.hash.slice(1) || "/");
  useEffect(() => {
    const on = () => setH(location.hash.slice(1) || "/");
    addEventListener("hashchange", on);
    return () => removeEventListener("hashchange", on);
  }, []);
  return h;
}

export const go = (path: string) => {
  location.hash = path;
};
