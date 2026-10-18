#!/bin/sh
# Runs inside the docshot image: soffice + ffmpeg only, no network.
cd /w/out || exit 1; IN=/w/in; export HOME=/tmp
lo() { soffice --headless --convert-to "$2" --outdir /tmp/lo "$1" >/dev/null 2>&1; f=$(ls /tmp/lo 2>/dev/null | head -1); if [ -n "$f" ]; then mv "/tmp/lo/$f" "./$3"; else echo "LO-FAIL $3 ($2)"; fi; rm -rf /tmp/lo; }
# Writer
for t in "doc:MS Word 97|x.doc" "dot:MS Word 97 Vorlage|x.dot" "dotx:MS Word 2007 XML Template|x.dotx" "odt|x.odt" "ott:writer8_template|x.ott" "fodt|x.fodt" "rtf|x.rtf" "uot:UOF text|x.uot" "sxw:StarOffice XML (Writer)|x.sxw" "html:HTML (StarWriter)|x.htm" "epub|x.epub" "odm:writerglobal8|x.odm"; do lo $IN/report.docx "${t%%|*}" "${t##*|}"; done
cp $IN/report.docx x.docx; cp $IN/report.docx x.docm; cp x.dotx x.dotm 2>/dev/null
# Calc
for t in "xls:MS Excel 97|x.xls" "xlt:MS Excel 97 Vorlage/Template|x.xlt" "xltx:Calc MS Excel 2007 XML Template|x.xltx" "xlsb:Calc MS Excel 2007 Binary|x.xlsb" "ods|x.ods" "ots:calc8_template|x.ots" "fods|x.fods" "csv|x.csv" "dif:DIF|x.dif" "slk:SYLK|x.slk" "dbf:dBase|x.dbf" "uos:UOF spreadsheet|x.uos" "sxc:StarOffice XML (Calc)|x.sxc"; do lo $IN/sheet.xlsx "${t%%|*}" "${t##*|}"; done
cp $IN/sheet.xlsx x.xlsx; cp $IN/sheet.xlsx x.xlsm; cp x.xltx x.xltm 2>/dev/null
# Impress
for t in "ppt:MS PowerPoint 97|x.ppt" "pps:MS PowerPoint 97 Autoplay|x.pps" "pot:MS PowerPoint 97 Vorlage|x.pot" "potx:Impress MS PowerPoint 2007 XML Template|x.potx" "ppsx:Impress MS PowerPoint 2007 XML AutoPlay|x.ppsx" "odp|x.odp" "otp:impress8_template|x.otp" "fodp|x.fodp" "uop:UOF presentation|x.uop" "sxi:StarOffice XML (Impress)|x.sxi"; do lo $IN/deck.pptx "${t%%|*}" "${t##*|}"; done
cp $IN/deck.pptx x.pptx; cp $IN/deck.pptx x.pptm; cp x.potx x.potm 2>/dev/null
# Draw: an SVG with a big sentinel → odg → the Draw formats
cat > x.svg <<'S'
<svg xmlns="http://www.w3.org/2000/svg" width="800" height="400"><rect width="800" height="400" fill="#fff"/><rect x="20" y="20" width="760" height="360" fill="none" stroke="#000" stroke-width="4"/><text x="60" y="220" font-family="DejaVu Sans" font-size="64" fill="#000">SENTINEL-DRAW-d4a1</text></svg>
S
lo x.svg "odg:draw8" x.odg
for t in "otg:draw8_template|x.otg" "fodg:OpenDocument Drawing Flat XML|x.fodg" "sxd:StarOffice XML (Draw)|x.sxd" "emf:draw_emf_Export|x.emf" "wmf:draw_wmf_Export|x.wmf" "eps:draw_eps_Export|x.eps" "svm:draw_svm_Export|x.svm" "met:draw_met_Export|x.met" "pct:draw_pct_Export|x.pct" "xpm:draw_xpm_Export|x.xpm" "ras:draw_ras_Export|x.ras"; do lo x.odg "${t%%|*}" "${t##*|}"; done
# Raster images via ffmpeg (text-bearing invoice)
for e in pcx ppm pgm pbm tga xbm bmp tif tiff png jpg jpeg gif webp; do ffmpeg -v error -y -i $IN/invoice.png "x.$e" || echo "FF-FAIL $e"; done
# Video with a burned-in sentinel
VF="drawtext=fontfile=/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf:text=SENTINEL-VIDEO-9e1c:fontsize=48:fontcolor=black:box=1:boxcolor=white:x=20:y=100"
for e in mp4 m4v mov qt webm mkv avi wmv flv mpg mpeg ts m2ts mts 3gp ogv; do
  case $e in 3gp) S=352x288;; *) S=640x360;; esac
  case $e in wmv) C="-c:v wmv2";; flv) C="-c:v flv";; mpg|mpeg|ts|m2ts|mts) C="-c:v mpeg2video";; ogv) C="-c:v libtheora";; 3gp) C="-c:v h263";; webm) C="-c:v libvpx-vp9";; avi) C="-c:v mpeg4";; qt) C="-c:v mpeg4 -f mov";; *) C="";; esac
  ffmpeg -v error -y -f lavfi -i "color=c=0xDDDDDD:s=$S:d=4:r=10" -vf "$VF" $C "x.$e" || echo "FF-FAIL $e"
done
# exotic rasters ffmpeg decodes
I=$IN/invoice.png
ffmpeg -v error -y -i $I -c:v sunrast -f image2 x.ras
ffmpeg -v error -y -i $I -c:v libaom-av1 -still-picture 1 -crf 30 x.avif
ffmpeg -v error -y -i $I -c:v libjxl x.jxl
ffmpeg -v error -y -i $I -c:v libopenjpeg x.jp2
ffmpeg -v error -y -i $I -c:v libopenjpeg -f image2 x.j2k
ffmpeg -v error -y -i $I x.qoi
ffmpeg -v error -y -i $I -pix_fmt gbrpf32le x.exr
ffmpeg -v error -y -i $I -c:v hdr -f image2 x.hdr
ffmpeg -v error -y -i $I -vf scale=256:-1 x.ico
for e in pam sgi xwd; do ffmpeg -v error -y -i $I x.$e; done
ffmpeg -v error -y -i $I -f image2 -c:v ppm x.pnm
cp x.ppt x.pps
# audio: tags, cover art on the mp3, an audio-only webm
ffmpeg -v error -y -f lavfi -i "sine=frequency=440:duration=8" -f lavfi -i "anoisesrc=d=8:a=0.05" -filter_complex "[0][1]amix=inputs=2,volume=3,aformat=channel_layouts=stereo" /tmp/a.wav
T="SENTINEL-AUDIO-a11d"
ffmpeg -v error -y -i /tmp/a.wav -i $I -map 0 -map 1 -c:a libmp3lame -c:v png -disposition:v attached_pic -metadata title=$T -metadata artist="Docshot Test" x.mp3
for e in flac ogg opus m4a wav; do ffmpeg -v error -y -i /tmp/a.wav -metadata title=$T -metadata artist="Docshot Test" x.$e; done
ffmpeg -v error -y -i /tmp/a.wav -c:a libvorbis -metadata title=$T -f webm x-audio-only.webm
# multipage TIFF and DjVu (two pages each)
DT="drawtext=fontfile=/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf:text=PAGE TWO:fontsize=80:x=100:y=500"
ffmpeg -v error -y -i $I /tmp/p1.tif; ffmpeg -v error -y -i $I -vf "$DT" /tmp/p2.tif; tiffcp /tmp/p1.tif /tmp/p2.tif x.tif
ffmpeg -v error -y -i $I /tmp/p1.ppm; ffmpeg -v error -y -i /tmp/p2.tif /tmp/p2.ppm
c44 /tmp/p1.ppm /tmp/p1.djvu; c44 /tmp/p2.ppm /tmp/p2.djvu; djvm -c x.djvu /tmp/p1.djvu /tmp/p2.djvu
# archives libarchive writes
cp $I /tmp/inv.png; cp $IN/report.docx /tmp/report.docx
(cd /tmp && bsdtar -a -cf /w/out/x.7z inv.png report.docx && bsdtar --format iso9660 -cf /w/out/x.iso inv.png report.docx \
  && bsdtar -a -cf /w/out/x.tar.xz inv.png report.docx && bsdtar -a -cf /w/out/x.tar.bz2 inv.png report.docx)
cp x.7z x.cb7
cp $IN/sample.tar.gz x.tar.gz; cp $IN/sample.tar.gz x.tgz; cp $IN/sample.jar x.jar; cp $IN/sample.eml x.eml
cp /usr/share/fonts/truetype/dejavu/DejaVuSans.ttf x.ttf
ls | wc -l
