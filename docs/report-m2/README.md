# Laporan Milestone 2 DikejarDeadline

- [PDF laporan](IF4031_M2_DikejarDeadline.pdf).
- Sumber LaTeX tersedia di main.tex, chapters/, dan appendices/.
- [Indeks bukti pengujian](evidence/README.md) dan [gambar laporan](figures/).

Bangun PDF dari direktori ini dengan LuaLaTeX dan latexmk.

    latexmk -lualatex -interaction=nonstopmode -halt-on-error -outdir=build main.tex
    cp build/main.pdf IF4031_M2_DikejarDeadline.pdf

Form Penggunaan AI disertakan dari form-penggunaan-ai-m2.pdf.
