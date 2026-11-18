# extract

<!-- docgen:begin id=internal/extract:overview scope=internal/extract hash=d0ef30ac29bb -->
The `extract` package provides functionality to pull documentation facts from source files without using artificial intelligence. It identifies declared symbols such as functions, types, and variables along with their signatures and doc comments, as well as environment variables read by the code. Each supported language has its own extractor implementation, using structured parsing where feasible (like Go, Terraform, YAML, and Python via its AST module) and line-based pattern matching for others.

The core types include `Symbol` for documented entities, `EnvVar` for environment variable reads, and `Result` for the output of an extraction operation. The `Extractor` interface defines how each language processor should behave, with methods for identifying the language and extracting information from a file. A registry of all extractors is maintained and made available through the `All` function, keyed by language name.

Language detection is handled by the `Language` function, which determines the appropriate extractor based on file extensions, shebangs, or specific file names like `values.yaml`. For environment variable detection, regular expressions are used per language to find calls to environment access functions. The package also includes helper functions for parsing comments and managing symbol hierarchies, particularly in YAML files where nested structures and documentation are common.
<!-- docgen:end id=internal/extract:overview -->

<!-- docgen:begin id=internal/extract:reference scope=internal/extract hash=d0ef30ac29bb -->
## Reference

### `extract.go`

- **`Symbol`** (struct): Symbol is one declared thing worth documenting.
- **`EnvVar`** (struct): EnvVar is an environment variable read by the code.
- **`Result`** (struct): Result is what an extractor found in one file.
- **`Extractor`** (interface): Extractor handles one language.
- **`Language`** (function): Language detects a file's language from its path and first line; "" if unsupported.
- **`All`** (function): All returns every extractor, keyed by language.
- **`EnvVars`** (function): EnvVars finds environment variable reads in a file.

### `golang.go`

- **`goExtractor.Language`** (method)
- **`goExtractor.Extract`** (method)

### `patterns.go`

- **`bashExtractor.Language`** (method)
- **`bashExtractor.Extract`** (method)
- **`csharpExtractor.Language`** (method)
- **`csharpExtractor.Extract`** (method)
- **`rustExtractor.Language`** (method)
- **`rustExtractor.Extract`** (method)
- **`rExtractor.Language`** (method)
- **`rExtractor.Extract`** (method)
- **`pythonRegexExtractor.Language`** (method)
- **`pythonRegexExtractor.Extract`** (method)

### `python.go`

- **`PythonAST`** (function): PythonAST extracts symbols from many Python files in one python3 run, using the standard library's ast module (exact signatures and docstrings). It returns nil and false when python3 is not available; callers then use the regex extractor.

### `python_ast.py`

- **`signature`** (function)
- **`walk`** (function)
- **`main`** (function)

### `terraform.go`

- **`terraformExtractor.Language`** (method)
- **`terraformExtractor.Extract`** (method): Extract records variables, outputs, resources, data sources, modules, providers and locals. Variable and output descriptions become their docs.

### `yaml.go`

- **`yamlExtractor.Language`** (method)
- **`yamlExtractor.Extract`** (method)
<!-- docgen:end id=internal/extract:reference -->
