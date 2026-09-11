"""Test package for the behaviour-detection recipes.

The recipes are standalone scripts in the parent directory rather than an
installed package, so tests need that directory on sys.path. Test modules
also import fixtures as a top-level module (``from fixtures import ...``)
rather than ``from tests.fixtures import ...``, so this directory itself
needs to be on sys.path too -- `unittest discover` adds its start directory
automatically, but `python3 -m unittest tests.test_x` does not. Importing
this package is what puts both directories there, under both invocation
styles.
"""
import os
import sys

_here = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(_here))
sys.path.insert(0, _here)
