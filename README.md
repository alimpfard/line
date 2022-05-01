# (Lib)Line

A full reimplementation of SerenityOS's [LibLine](https://github.com/SerenityOS/serenity/tree/master/Userland/Libraries/LibLine) in Go.

LibLine is a full-featured terminal line editor with support for:
- Flexible autocompletion
- Live prompt and buffer update/stylisation
- Multiline editing
- and more.

The API is a complete clone of SerenityOS's LibLine, an example can be found in [example/](example/).

This implementation is currently incomplete, and has missing or otherwise buggy features:
- [ ] Interrupt and EOF handling isn't quite correct yet (buggy)
- [ ] A lot of keybindings are missing (incomplete)
- [ ] Bracketed paste is not implemented yet
- [ ] LibLine's history file format is not implemented yet
- [ ] And probably many more bugs not yet encountered.
