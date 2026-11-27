#!/bin/sh
set -eu

printf 'sf-proctor mutant: %s\n' "proctor mutate can't run these mutants yet: it reads the page's keys from its own environment, which an up script can't change, and each start of the stack signs in anew with new keys. Run a mutant by hand instead: apply its edit, run make proctor, and check that a scenario fails (.claude/rules/proctor.md)." >&2
exit 2
