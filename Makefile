SDK_ROOT := $(shell makefile='$(subst ','"'"',$(MAKEFILE_LIST))'; makefile=$$(printf '%s' "$$makefile" | sed 's/^ //'); cd "$$(dirname "$$makefile")" && pwd)
PYTHON ?= python3
.PHONY: deps build check test package fmt fmt-check clean
deps build check test package fmt fmt-check clean:
	@$(PYTHON) "$(SDK_ROOT)/tools/build.py" "$@"
