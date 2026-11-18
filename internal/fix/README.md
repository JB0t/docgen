# fix

<!-- docgen:begin id=internal/fix:overview scope=internal/fix hash=5c5da266b327 -->
Package fix handles corrections to hand-written documentation that docgen check has found wrong. It only acts on specific kinds of findings outside generated sections. The package edits text a person wrote, so it is opt-in and changes only named lines. Every edit starts with a loud marker comment visible in git.

The main functions identify fixable findings, build change requests for the model, mark replacements with headers, and count edited regions. Fixable filters findings to those that can be corrected. Request formats the prompt for the model. Mark ensures replacements start with marker lines. MarkerLine builds the header comment. Count reports how many edits were made.

Fixable uses generatedLines to skip findings inside docgen:begin/end sections. The marker regex matches the exact comment format. The model's changes are limited to the lines named in findings. Backticks in marker lines are stripped to avoid false references. The package tracks edits with a consistent comment format.
<!-- docgen:end id=internal/fix:overview -->

<!-- docgen:begin id=internal/fix:reference scope=internal/fix hash=5c5da266b327 -->
## Reference

### `fix.go`

- **`Marker`** (constant): Marker starts the comment put above every edited region.
- **`Fixable`** (function): Fixable keeps the findings for one doc that --fix may act on: fixable kinds, outside generated sections (those are redrafted, not patched).
- **`Request`** (function): Request is the change request handed to the code engine for one doc.
- **`Mark`** (function): Mark makes sure every block's replacement starts with a marker line, so an edit the model forgot to label is still visible. Backticks inside marker lines are removed, because the checker would read them as references.
- **`MarkerLine`** (function): MarkerLine formats the comment put above an edited region.
- **`Count`** (function): Count reports how many marker lines a text holds, for the run summary.
<!-- docgen:end id=internal/fix:reference -->
