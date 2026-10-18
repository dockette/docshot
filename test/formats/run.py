import subprocess,sys,os,re,json,concurrent.futures as cf
X=sys.argv[1]; files=[]
for d in ("gen/out","corpus"):
    if not os.path.isdir(f"{X}/{d}"): continue
    for f in sorted(os.listdir(f"{X}/{d}")):
        if not f.endswith((".sh",)): files.append(f"{X}/{d}/{f}")
def one(path):
    name=os.path.basename(path); out=f"{X}/png/{name}.png"
    r=subprocess.run(["curl","-s","-m","300","-o",out,"-D","-","-w","%{http_code}","-F",f"file=@{path}","http://localhost:9994/upload?width=1200"],capture_output=True,text=True)
    hdr=r.stdout; code=hdr.strip().splitlines()[-1][-3:] if hdr.strip() else "000"
    h={k.lower():v.strip() for k,_,v in (l.partition(":") for l in hdr.splitlines() if ":" in l)}
    row={"file":name,"ext":name.split(".",1)[-1] if "." in name else name,"code":code,"path":h.get("x-render-path","-"),"type":h.get("x-source-type","-"),"pages":h.get("x-page-count","-"),"ms":h.get("x-render-ms","-")}
    if code!="200":
        row["result"]="ERR "+open(out,errors="replace").read()[:110].replace("\n"," "); return row
    ocr=subprocess.run(["curl","-s","-m","120","-T",out,"-H","Accept: text/plain","http://localhost:9998/tika"],capture_output=True,text=True).stdout
    norm=ocr.upper().replace(" ","").replace("\n","")
    m=re.search(r"SENTINEL-?[A-Z0-9]+-?[0-9A-F]{4}",norm.replace("O","0").replace("0","O") if False else norm)
    if "SENTINEL" in norm: row["result"]="OK sentinel"
    elif len(re.sub(r"\W","",ocr))>=3: row["result"]="TEXT "+re.sub(r"\s+"," ",ocr.strip())[:40]
    else: row["result"]="BLANK?"
    return row
with cf.ThreadPoolExecutor(4) as ex: rows=list(ex.map(one,files))
json.dump(rows,open(f"{X}/results.json","w"),indent=1)
for r in rows: print(f"{r['file']:20} {r['code']} {r['path']:18} {r['type']:10} {r['pages']:>3} {r['ms']:>6}  {r['result']}")
ok=sum(r["code"]=="200" for r in rows); sent=sum(r.get("result","").startswith("OK") for r in rows)
print(f"\n{len(rows)} files: {ok} rendered (HTTP 200), {sent} with the sentinel read back by OCR, {len(rows)-ok} failed")
sys.exit(0 if ok==len(rows) else 1)
