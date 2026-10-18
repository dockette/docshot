#!/usr/bin/env python3
"""Hand-written fixtures (text, web, mail, data formats) + decoding of *.uu
corpus files. Usage: fixtures.py <OUT>  — writes into <OUT>/gen/out, decodes
<OUT>/corpus/*.uu. Runs after gen.sh (it reuses gen.sh's x.png, x.jpg, x.ttf)."""
import base64, binascii, bz2, glob, gzip, hashlib, json, lzma, os, sys, zipfile

OUT = sys.argv[1]
O = f"{OUT}/gen/out"
IN = f"{OUT}/gen/in"


def tok(ext):
    return "SENTINEL-T" + ext.upper().replace("-", "")[:6] + "-" + hashlib.md5(ext.encode()).hexdigest()[:4]


def w(name, data, mode="w"):
    if os.path.exists(f"{O}/{name}"):  # gen.sh files belong to the container's user
        os.remove(f"{O}/{name}")
    with open(f"{O}/{name}", mode) as f:
        f.write(data)


# Plain text and source code: one sentinel per extension.
TEXT = ("txt text log jsonl ndjson xml yaml yml toml ini cfg conf env properties srt vtt tex rst adoc org diff "
        "patch py java go rs c h cc cpp hpp cs js mjs tsx jsx rb php pl lua kt kts swift scala sh bash zsh ps1 "
        "bat sql css scss less r m dart groovy gradle vue svelte").split()
for e in TEXT:
    body = '<doc><id>%s</id></doc>' if e == "xml" else "Header line\n%s\nlast line with žluťoučký kůň"
    w(f"x.{e}", body % tok(e))
for n in ("Dockerfile", "Makefile"):
    w(n, f"# {tok(n.lower())}\nFROM scratch\n")
for e in ("md", "markdown", "mdown", "mkd"):
    w(f"x.{e}", f"# {tok(e)}\n\n| a | b |\n|---|---|\n| 1 | 2 |\n")

# Web and mail
w("x.html", f"<!doctype html><html><body><h1>{tok('html')}</h1><p>žluťoučký kůň</p></body></html>")
w("x.xhtml", f'<?xml version="1.0" encoding="UTF-8"?><html xmlns="http://www.w3.org/1999/xhtml"><head><title>x</title></head><body><h1>{tok("xhtml")}</h1></body></html>')
w("x.mht", f'''From: <Saved by Blink>
Subject: x
MIME-Version: 1.0
Content-Type: multipart/related; type="text/html"; boundary="B"

--B
Content-Type: text/html; charset="utf-8"
Content-Location: http://example.com/

<html><body><h1>{tok("mht")}</h1></body></html>
--B--
''')
eml = open(f"{IN}/sample.eml").read()
w("x.mbox", "From sender@example.com Mon Aug 31 09:00:00 2026\n" + eml.replace("predmet", tok("mbox")))
w("x.mml", '<math xmlns="http://www.w3.org/1998/Math/MathML"><mi>x</mi><mo>=</mo><mn>42</mn></math>')
w("x.xpm", '/* XPM */\nstatic char *x[] = {\n"8 8 2 1",\n"  c #FFFFFF",\n". c #000000",\n' + ",\n".join(['"..  ..  "'] * 8) + "};\n")

# Data formats with their own renderers
png = base64.b64encode(open(f"{IN}/invoice.png", "rb").read()).decode()
w("table.csv", "id;name;amount;city\n" + "\n".join(f"{i};SENTINEL-CSV-c5a1 row {i};{i*12.5:.2f};Brno" for i in range(1, 61)))
w("table.tsv", "id\tname\tvalue\n" + "\n".join(f"{i}\tSENTINEL-TSV-75a1\t{i}" for i in range(1, 30)))
w("data.json", json.dumps({"id": "SENTINEL-JSON-15a1", "items": [{"n": i, "name": "žluťoučký kůň"} for i in range(30)]},
                       separators=(",", ":"), ensure_ascii=False))
w("x.ipynb", json.dumps({"nbformat": 4, "nbformat_minor": 5, "metadata": {"kernelspec": {"display_name": "Python 3"}}, "cells": [
    {"cell_type": "markdown", "metadata": {}, "source": ["# SENTINEL-NB-1a2b\n", "Notebook with **markdown**, code and a plot."]},
    {"cell_type": "code", "metadata": {}, "execution_count": 1, "source": ["print('hello')\n", "1+1"],
     "outputs": [{"output_type": "stream", "name": "stdout", "text": ["hello\n"]},
                 {"output_type": "execute_result", "execution_count": 1, "metadata": {}, "data": {"text/plain": ["2"]}}]},
    {"cell_type": "code", "metadata": {}, "execution_count": 2, "source": ["plot()"],
     "outputs": [{"output_type": "display_data", "metadata": {}, "data": {"image/png": png, "text/plain": ["<Figure>"]}}]}]}))
w("x.ics", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nX-WR-CALNAME:Team calendar\r\nBEGIN:VEVENT\r\nDTSTART:20261020T090000Z\r\n"
           "DTEND:20261020T100000Z\r\nSUMMARY:SENTINEL-ICS-1c5a planning\r\nLOCATION:Praha, zasedačka 3\r\n"
           "ORGANIZER:mailto:boss@example.com\r\nDESCRIPTION:Agenda:\\n- roadmap\\n- budget\r\nEND:VEVENT\r\n"
           "BEGIN:VEVENT\r\nDTSTART:20261021\r\nSUMMARY:All-day review\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
w("x.vcf", "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Jana Nováková\r\nN:Nováková;Jana;;;\r\nORG:SENTINEL-VCF-5ca1\r\nTITLE:CTO\r\n"
           "TEL;TYPE=work:+420 123 456 789\r\nEMAIL;TYPE=work:jana@example.com\r\nEND:VCARD\r\n"
           "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Petr Svoboda\r\nEMAIL:petr@example.com\r\nEND:VCARD\r\n")
w("x.gv", 'digraph G { rankdir=LR; node [shape=box, fontname="DejaVu Sans"]; a [label="SENTINEL-GV-9a1f"]; a -> b -> c; b -> d; }')
w("graph.dot", '// graph\ndigraph G { a [label="SENTINEL-DOT-d07a"]; a -> b; }')
w("y.svg", '<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 200"><defs><linearGradient id="g">'
           '<stop offset="0" stop-color="#2563eb"/><stop offset="1" stop-color="#a855f7"/></linearGradient></defs>'
           '<rect width="400" height="200" rx="20" fill="url(#g)"/><text x="30" y="115" font-size="30" fill="#fff" '
           'font-family="DejaVu Sans">SENTINEL-SVG-5a9b</text></svg>')

# Single compressed files and a comic book (zip of images)
txt = "SENTINEL-GZIP-9z1a\nhello compressed\n".encode()
w("note.txt.gz", gzip.compress(txt), "wb")
w("note.txt.xz", lzma.compress(txt), "wb")
w("note.txt.bz2", bz2.compress(txt), "wb")
with zipfile.ZipFile(f"{O}/x.cbz", "w") as z:
    z.write(f"{O}/x.png", "01.png")
    z.write(f"{O}/x.jpg", "02.jpg")

# XPS: one FixedPage with a shape and text in an embedded font
with zipfile.ZipFile(f"{O}/x.xps", "w") as z:
    z.writestr("[Content_Types].xml", '<?xml version="1.0" encoding="utf-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="fdseq" ContentType="application/vnd.ms-package.xps-fixeddocumentsequence+xml"/><Default Extension="fdoc" ContentType="application/vnd.ms-package.xps-fixeddocument+xml"/><Default Extension="fpage" ContentType="application/vnd.ms-package.xps-fixedpage+xml"/><Default Extension="ttf" ContentType="application/vnd.ms-opentype"/></Types>')
    z.writestr("_rels/.rels", '<?xml version="1.0" encoding="utf-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Type="http://schemas.microsoft.com/xps/2005/06/fixedrepresentation" Target="/FixedDocSeq.fdseq" Id="R0"/></Relationships>')
    z.writestr("FixedDocSeq.fdseq", '<FixedDocumentSequence xmlns="http://schemas.microsoft.com/xps/2005/06"><DocumentReference Source="/Documents/1/FixedDoc.fdoc"/></FixedDocumentSequence>')
    z.writestr("Documents/1/FixedDoc.fdoc", '<FixedDocument xmlns="http://schemas.microsoft.com/xps/2005/06"><PageContent Source="/Documents/1/Pages/1.fpage"/></FixedDocument>')
    z.writestr("Documents/1/Pages/1.fpage", '<FixedPage xmlns="http://schemas.microsoft.com/xps/2005/06" Width="816" Height="1056" xml:lang="en"><Path Data="M 50,50 L 760,50 760,250 50,250 Z" Fill="#FFDBEAFE" Stroke="#FF2563EB" StrokeThickness="4"/><Glyphs FontUri="/Resources/font.ttf" FontRenderingEmSize="40" OriginX="80" OriginY="170" Fill="#FF000000" UnicodeString="SENTINEL-XPS-a7c1"/></FixedPage>')
    z.write(f"{O}/x.ttf", "Resources/font.ttf")

# Corpus: uudecode, and a .cbr from the RAR 4 sample
for f in glob.glob(f"{OUT}/corpus/*.uu"):
    data, started = b"", False
    for line in open(f, "rb"):
        if line.startswith(b"begin"):
            started = True
            continue
        if line.strip() == b"end":
            break
        if started:
            try:
                data += binascii.a2b_uu(line)
            except binascii.Error:
                pass
    open(f[:-3], "wb").write(data)
    os.remove(f)
if os.path.exists(f"{OUT}/corpus/x.rar"):
    open(f"{OUT}/corpus/x.cbr", "wb").write(open(f"{OUT}/corpus/x.rar", "rb").read())
print(len(os.listdir(O)), "generated,", len(os.listdir(f"{OUT}/corpus")), "corpus")
