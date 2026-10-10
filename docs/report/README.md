# Laporan Milestone 1 DikejarDeadline

- [PDF laporan](IF4031_M1_DikejarDeadline.pdf).
- Sumber LaTeX: `main.tex`, `setup.tex`, `chapters/`, dan `appendices/`.
- Bukti pengujian: `evidence/` dan `figures/`.

Build dari folder ini dengan LuaLaTeX dan latexmk:

```sh
latexmk -lualatex -interaction=nonstopmode -halt-on-error -outdir=build main.tex
cp build/main.pdf IF4031_M1_DikejarDeadline.pdf
```
