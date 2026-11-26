/**
 * Peer part-bitmap helpers for the source list.
 *
 * A source's bitmap arrives as hex because a 6.5 GB file has ~6700 parts: a JSON
 * boolean array would be tens of kilobytes per source per poll, while the packed
 * bitmap stays under a kilobyte. The bar cannot become 6700 DOM nodes inside a
 * 200px cell either, so neighbouring parts share a bin and each bin is drawn with
 * the fraction of parts it covers.
 *
 * Bit order matters: protocol.BitField packs MSB first (GetBit uses
 * `0x80 >> (index & 7)`), so bit 0 of a part list is the high bit of byte 0.
 * Getting this backwards would paint a different part of the file without any
 * visible error, which is why the decode lives here with a testable signature.
 */

/** decodePieceHex turns the packed bitmap into bytes, most significant bit first. */
export function decodePieceHex(hex: string): Uint8Array {
  const clean = hex.length % 2 === 0 ? hex : `0${hex}`
  const out = new Uint8Array(clean.length / 2)
  for (let i = 0; i < out.length; i += 1) {
    const byte = Number.parseInt(clean.slice(i * 2, i * 2 + 2), 16)
    if (Number.isNaN(byte)) return out.subarray(0, i)
    out[i] = byte
  }
  return out
}

export interface PieceCoverage {
  /** Fraction (0..1) of each bin covered by parts the peer owns. */
  bars: number[]
  /** Total parts owned, for the tooltip. */
  have: number
}

/**
 * pieceCoverage bins a peer's bitmap. `pieces` is the part count the peer
 * declared; `maxBins` caps the DOM cost.
 */
export function pieceCoverage(hex: string, pieces: number, maxBins = 96): PieceCoverage {
  if (pieces <= 0) return { bars: [], have: 0 }
  const bytes = decodePieceHex(hex)
  const bins = Math.min(pieces, Math.max(1, maxBins))
  const counts = new Array<number>(bins).fill(0)
  let have = 0
  for (let i = 0; i < pieces; i += 1) {
    const set = ((bytes[i >> 3] ?? 0) >> (7 - (i & 7))) & 1
    if (!set) continue
    have += 1
    counts[Math.min(bins - 1, Math.floor((i * bins) / pieces))] += 1
  }
  const perBin = pieces / bins
  return { bars: counts.map((count) => Math.min(1, count / perBin)), have }
}
