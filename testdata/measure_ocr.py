#!/usr/bin/env python3
"""
스캔 PDF OCR 정확도 측정.

gen_corpus.py 가 만든 스캔본은 원본 텍스트(ground truth)를 알고 있으므로
OCR 결과와 대조해 실제 정확도를 숫자로 낼 수 있다.

측정 지표
 - 문자 정확도 : 1 - (편집거리 / 원문길이), 공백 제거 후
 - 검색어 재현율: 실무에서 실제로 검색할 법한 키워드가 OCR 결과에 남아있는 비율
                 (우리 목적은 원문 복원이 아니라 "검색이 걸리는가" 이므로 이쪽이 더 중요)
"""
import os
import re
import subprocess
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from gen_corpus import DEUNGGI, JEUNGGEO  # noqa: E402

HERE = os.path.dirname(os.path.abspath(__file__))
CORPUS = os.path.join(HERE, "corpus")

CASES = [
    {
        "pdf": "증거자료/등기부등본_강남테헤란로_스캔.pdf",
        "truth": ["등기사항전부증명서"] + DEUNGGI,
        "keywords": ["등기사항전부증명서", "테헤란로", "소유권보존", "소유권이전",
                     "근저당권설정", "채권최고액", "주식회사", "강남구"],
    },
    {
        "pdf": "증거자료/갑제3호증_물품공급계약서사본.pdf",
        "truth": ["물품공급계약서"] + JEUNGGEO,
        "keywords": ["물품공급계약서", "공급자", "수요자", "지연손해금",
                     "대표이사", "삼억이천만원", "정산"],
    },
]


def levenshtein(a: str, b: str) -> int:
    if len(a) < len(b):
        a, b = b, a
    prev = list(range(len(b) + 1))
    for i, ca in enumerate(a, 1):
        cur = [i]
        for j, cb in enumerate(b, 1):
            cur.append(min(prev[j] + 1, cur[j - 1] + 1, prev[j - 1] + (ca != cb)))
        prev = cur
    return prev[-1]


def ocr(pdf_path: str, dpi: int, psm: str) -> str:
    with tempfile.TemporaryDirectory() as td:
        png = os.path.join(td, "p.png")
        subprocess.run(
            ["mutool", "draw", "-F", "png", "-r", str(dpi), "-o", png, pdf_path],
            check=True, capture_output=True)
        out = subprocess.run(
            ["tesseract", png, "stdout", "-l", "kor", "--psm", psm],
            check=True, capture_output=True, text=True)
        return out.stdout


def norm(s: str) -> str:
    return re.sub(r"\s+", "", s)


def main():
    print(f"{'문서':<26} {'DPI':>4} {'PSM':>4} {'문자정확도':>9} {'검색어재현율':>11}  누락 키워드")
    print("-" * 104)

    best = {}
    for case in CASES:
        pdf = os.path.join(CORPUS, case["pdf"])
        truth = norm("\n".join(case["truth"]))
        name = os.path.basename(case["pdf"])[:24]

        for dpi in (150, 300):
            for psm in ("3", "6"):
                try:
                    text = ocr(pdf, dpi, psm)
                except subprocess.CalledProcessError as e:
                    print(f"{name:<26} {dpi:>4} {psm:>4}  ERROR {e}")
                    continue
                got = norm(text)
                dist = levenshtein(truth, got)
                char_acc = max(0.0, 1 - dist / max(1, len(truth)))

                hit = [k for k in case["keywords"] if k in got]
                miss = [k for k in case["keywords"] if k not in got]
                recall = len(hit) / len(case["keywords"])

                key = case["pdf"]
                if recall > best.get(key, (-1,))[0]:
                    best[key] = (recall, char_acc, dpi, psm)

                print(f"{name:<26} {dpi:>4} {psm:>4} {char_acc:>8.1%} {recall:>11.1%}  "
                      f"{', '.join(miss) if miss else '-'}")

    print("\n=== 문서별 최적 설정 ===")
    for k, (rec, acc, dpi, psm) in best.items():
        print(f"  {os.path.basename(k):<40} dpi={dpi} psm={psm}  "
              f"검색어재현율 {rec:.0%} / 문자정확도 {acc:.0%}")


if __name__ == "__main__":
    main()
