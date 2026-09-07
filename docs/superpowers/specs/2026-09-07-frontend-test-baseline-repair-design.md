# Frontend Test Baseline Repair

## Goal

Restore the frontend Vitest baseline by correcting one stale Writer Markdown
expectation and providing the browser API required by the Skill Installed View
tests.

## Writer Markdown Contract

`writerMarkdownForEditing` intentionally removes standalone system-anchor lines
for both headings and images. Persisted identifiers are mapped to editable DOM
targets and restored at the save boundary, so retaining an image anchor as an
editable standalone block would contradict the editor architecture.

The failing unit test will be renamed to describe all target anchors, and its
expected editable Markdown will omit the image anchor. Production Markdown
transformation code will not change. Existing save-round-trip and editor
integration tests continue to verify that image identifiers survive editing.

## Skill Installed View Test Environment

The component test renders a real Ant Design table. Ant Design's responsive
observer requires `window.matchMedia`, which jsdom does not implement. The test
file will install the same local `matchMedia` mock used by adjacent Ant Design
tests in this repository.

The mock stays local instead of changing the global Vitest environment, keeping
the repair scoped to the test that requires it. Production component code and
runtime browser behavior will not change.

## Testing

First reproduce both focused failures. After updating the tests, run both
focused suites plus the Writer editor integration suite, then run TypeScript,
ESLint, and the complete frontend Vitest suite. The complete suite must report
zero failed tests before the changes are committed.
