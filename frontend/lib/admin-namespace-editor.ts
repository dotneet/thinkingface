/**
 * Inline quota-editor target after the listing's visible rows change.
 *
 * The editor is not a modal: search, paging and refresh all stay usable
 * while a row is being edited. `editing` also disables every other Edit
 * button, so leaving it set after the row has left the page (search,
 * another page, or a 404 refresh) hides the editor and locks the table.
 */
export function namespaceEditorTargetAfterRowsChange(
  editing: string | null,
  namespaces: readonly string[],
): string | null {
  if (editing === null || namespaces.includes(editing)) return editing;
  return null;
}
