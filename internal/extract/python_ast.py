"""docgen's Python fact extractor: reads JSON {"files": [paths]} on stdin and prints
symbols (functions, classes, methods with signatures and docstrings) as JSON lines.
Uses only the standard library so it runs on any python3 >= 3.8."""
import ast
import json
import sys


def signature(node):
    try:
        args = ast.unparse(node.args)
    except Exception:
        args = "..."
    returns = ""
    if getattr(node, "returns", None) is not None:
        try:
            returns = " -> " + ast.unparse(node.returns)
        except Exception:
            pass
    prefix = "async def " if isinstance(node, ast.AsyncFunctionDef) else "def "
    return f"{prefix}{node.name}({args}){returns}"


def walk(path, tree):
    module_doc = ast.get_docstring(tree)
    name = path.rsplit("/", 1)[-1]
    yield {"name": name, "kind": "module", "line": 1, "doc": module_doc or "", "exported": True}
    for node in tree.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            yield {"name": node.name, "kind": "function", "line": node.lineno, "signature": signature(node),
                   "doc": ast.get_docstring(node) or "", "exported": not node.name.startswith("_")}
        elif isinstance(node, ast.ClassDef):
            bases = ", ".join(ast.unparse(base) for base in node.bases) if hasattr(ast, "unparse") else ""
            yield {"name": node.name, "kind": "class", "line": node.lineno, "signature": f"class {node.name}({bases})",
                   "doc": ast.get_docstring(node) or "", "exported": not node.name.startswith("_")}
            for item in node.body:
                if isinstance(item, (ast.FunctionDef, ast.AsyncFunctionDef)):
                    yield {"name": item.name, "kind": "method", "parent": node.name, "line": item.lineno,
                           "signature": signature(item), "doc": ast.get_docstring(item) or "",
                           "exported": not item.name.startswith("_") or item.name == "__init__"}


def main():
    request = json.load(sys.stdin)
    for path in request["files"]:
        try:
            with open(path, encoding="utf-8", errors="replace") as handle:
                tree = ast.parse(handle.read(), filename=path)
        except (SyntaxError, ValueError, OSError):
            print(json.dumps({"file": path, "error": True}))
            continue
        for symbol in walk(path, tree):
            symbol["file"] = path
            print(json.dumps(symbol))


if __name__ == "__main__":
    main()
