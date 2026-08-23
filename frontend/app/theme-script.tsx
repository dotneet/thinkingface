const SCRIPT = `
(function () {
  try {
    var pref = localStorage.getItem("tf-theme");
    if (pref === "light" || pref === "dark") {
      document.documentElement.setAttribute("data-theme", pref);
    }
  } catch (e) {}
})();
`;

export function ThemeScript() {
  return (
    <script
      // A module-local constant with no interpolation, injected as an inline
      // blocking script because the theme has to be applied before first paint
      // (otherwise the page flashes light before switching to dark).
      // biome-ignore lint/security/noDangerouslySetInnerHtml: static, non-interpolated markup that must run before first paint
      dangerouslySetInnerHTML={{ __html: SCRIPT }}
    />
  );
}
