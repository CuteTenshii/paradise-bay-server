# Contributing

## Getting game files

Download and extract the Appx as described [in the readme](README.md).

## Decompiling Lua scripts

*Paradise Bay*, at least the game UI and engine, was made with Lua (and C on Windows).

You can use [viruscamp/luadec](https://github.com/viruscamp/luadec) to decompile all the `.dat` files, which are Lua bytecode files.
For the executable I personally use [Binary Ninja](https://binary.ninja/).

## Debugging

Any debugger works, I used VS debugger, and DBGENG on Binary Ninja.