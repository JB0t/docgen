# code

<!-- docgen:begin id=internal/code:overview scope=internal/code hash=72eae09e59e3 -->
Package code handles source code prompts and patches for documentation generation. It builds bounded source prompts from file scopes or related files, and validates model-generated changes. The package supports two patch formats: unified diffs and search/replace blocks. Search/replace blocks are preferred because they go through the same validation as diffs but avoid line-counting errors common in model diffs.

The package provides functions to collect source files from scopes or find related files based on a request. It builds prompts with bounded token counts and cleans or repairs model output. Functions parse search/replace blocks, apply them to copies of files, and generate diffs for validation. The diff validation ensures changes stay within requested scopes and can be applied cleanly with git.

The core logic for applying blocks works in memory first, then generates a unified diff for validation. The diff is checked against the repository root and validated for path scope. The package also includes utilities to clean and repair model diffs, fixing common mistakes like incorrect line counts or missing whitespace. Related file discovery uses term matching against paths, symbols, and file contents to find relevant files without model involvement.
<!-- docgen:end id=internal/code:overview -->

<!-- docgen:begin id=internal/code:reference scope=internal/code hash=72eae09e59e3 -->
## Reference

### `blocks.go`

- **`Format`** (type): Format is how the model is asked to express a change.
- **`FormatDiff`** (constant): FormatDiff asks for a unified diff: compact, but hunk headers and context lines are easy for small models to get wrong (RepairPatch fixes the common slips).
- **`FormatBlocks`** (constant): FormatBlocks asks for search/replace blocks: the model copies the lines to replace and writes the new ones, with no line counting. docgen applies them and makes the diff itself, so the result goes through exactly the same checks as a model diff.
- **`Block`** (struct): Block is one search/replace edit to a repo-relative file.
- **`ParseBlocks`** (function): ParseBlocks reads search/replace blocks from a model answer. Fences and prose between blocks are ignored; the path is the last non-empty line before a block.
- **`ApplyBlocks`** (function): ApplyBlocks applies blocks to copies of the files and returns the result as a unified diff with git-style headers, without touching the repository. The diff goes through ValidatePaths and git apply --check like a model-written one.
- **`ApplyBlocksContents`** (function): ApplyBlocksContents applies blocks in memory: the new content per repo-relative path, the paths in first-seen order, and whether each existed before.

### `code.go`

- **`Prompt`** (function): Prompt collects source from scopes without exceeding maxToks (estimated with llm.EstimateTokens). Scopes are files or directories relative to root.
- **`PromptFiles`** (function): PromptFiles builds the request prompt over exactly these repo-relative files.
- **`FileBlock`** (function): FileBlock returns one file's prompt block, or "" for a file the model should not see (binary or invalid UTF-8).
- **`SourceFiles`** (function): SourceFiles lists the source files under scopes (files or directories relative to root), excluding configured paths, non-source extensions and secret-looking names.
- **`CleanPatch`** (function): CleanPatch removes an optional Markdown diff fence and rejects non-diff output. Trailing spaces are kept: a hunk's last line can be a blank context line (" "), and trimming it leaves the hunk a line short, which git reports as a corrupt patch.
- **`RepairPatch`** (function): RepairPatch fixes the mistakes models make in otherwise good diffs, which git rejects as "corrupt patch": hunk headers whose line counts do not match the hunk, blank context lines written without their leading space, and text after the last hunk. It returns the patch and a description of each fix.
- **`ValidatePaths`** (function): ValidatePaths ensures every changed file is inside one of the requested scopes.

### `related.go`

- **`Related`** (function): Related picks the source files a request most likely concerns, in rank order, and stops adding files once the prompt would exceed maxToks. It needs no model: the request's words are matched against paths, symbol names and doc comments from the facts, and against file contents with git grep. The returned terms are the words that were searched for, so a caller can say why files were chosen.
- **`Terms`** (function): Terms splits a request into search words: identifiers and words of at least three letters, lower-cased, with camelCase and snake_case split into their parts as well as kept whole, minus common English filler.
<!-- docgen:end id=internal/code:reference -->
