package backtest

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"

	"piquant-live/internal/event"
)

// PQBinSourceReal marks a partition converted from a real exchange archive. It lives in the header's first
// reserved byte (offset 25); partitions written by SavePQBin (including the synthetic placeholders) leave it 0.
const PQBinSourceReal = 'R'

// PQBinIsReal reports whether a partition file was imported from a real archive.
func PQBinIsReal(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	var hdr [PQBinHeaderSize]byte
	if _, err := f.Read(hdr[:]); err != nil {
		return false, err
	}
	if string(hdr[0:6]) != string(PQBinMagic[:]) {
		return false, errors.New("invalid pqbin magic header")
	}
	return hdr[25] == PQBinSourceReal, nil
}

// ConvertZipToPQBin streams a Binance aggTrades zip into a .pqbin partition without holding the month in memory
// (a liquid month is tens of millions of trades). Records use the same 40-byte layout as SavePQBin. The file is
// written to path+".part" and renamed when complete. It returns the number of ticks.
func ConvertZipToPQBin(zipPath, path, symbol string, year, month int) (uint64, error) {
	n, _, err := ConvertZipToPQBinStats(zipPath, path, symbol, year, month)
	return n, err
}

// ConvertZipToPQBinStats is ConvertZipToPQBin and also returns how many out-of-order timestamps were clamped.
func ConvertZipToPQBinStats(zipPath, path, symbol string, year, month int) (uint64, uint64, error) {
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return 0, 0, err
	}
	defer os.Remove(tmp)
	bw := bufio.NewWriterSize(f, 1<<20)
	var hdrBuf [PQBinHeaderSize]byte
	if _, err := bw.Write(hdrBuf[:]); err != nil { // placeholder, rewritten at the end
		f.Close()
		return 0, 0, err
	}
	var (
		rec          [PQBinRecordSize]byte
		n            uint64
		startNs, end int64
		minP, maxP   = math.Inf(1), math.Inf(-1)
		lastNs       int64
		clamped      uint64
	)
	err = StreamZipArchive(zipPath, func(t *AggTradeTick) error {
		if t.Price <= 0 || t.Quantity <= 0 {
			return nil
		}
		if t.TimestampNs < lastNs {
			// Binance stamps a few trades slightly out of order. Clamp small steps so time never runs
			// backwards; a step over one second means a corrupt or unsorted archive.
			if lastNs-t.TimestampNs > 1_000_000_000 {
				return fmt.Errorf("tick %d goes back %d ms", t.TradeID, (lastNs-t.TimestampNs)/1_000_000)
			}
			t.TimestampNs = lastNs
			clamped++
		}
		lastNs = t.TimestampNs
		binary.LittleEndian.PutUint64(rec[0:8], t.TradeID)
		binary.LittleEndian.PutUint64(rec[8:16], uint64(int64(math.Round(t.Price*float64(event.PriceScale)))))
		binary.LittleEndian.PutUint64(rec[16:24], uint64(int64(math.Round(t.Quantity*float64(event.QtyScale)))))
		binary.LittleEndian.PutUint64(rec[24:32], uint64(t.TimestampNs))
		binary.LittleEndian.PutUint32(rec[32:36], uint32(t.FirstTradeID))
		binary.LittleEndian.PutUint32(rec[36:40], uint32(t.LastTradeID))
		var flags uint8
		if t.IsBuyerMaker {
			flags |= 1
		}
		if t.IsBestMatch {
			flags |= 2
		}
		rec[39] = flags // same layout as SavePQBin: the flag byte replaces the top byte of LastTradeID
		if n == 0 {
			startNs = t.TimestampNs
		}
		end = t.TimestampNs
		minP, maxP = math.Min(minP, t.Price), math.Max(maxP, t.Price)
		n++
		_, werr := bw.Write(rec[:])
		return werr
	})
	if err == nil {
		err = bw.Flush()
	}
	if err == nil && n == 0 {
		err = errors.New("archive has no ticks")
	}
	if err != nil {
		f.Close()
		return 0, 0, err
	}
	copy(hdrBuf[0:6], PQBinMagic[:])
	copy(hdrBuf[6:22], strings.ToUpper(symbol))
	binary.LittleEndian.PutUint16(hdrBuf[22:24], uint16(year))
	hdrBuf[24] = uint8(month)
	hdrBuf[25] = PQBinSourceReal
	binary.LittleEndian.PutUint64(hdrBuf[30:38], n)
	binary.LittleEndian.PutUint64(hdrBuf[38:46], uint64(startNs))
	binary.LittleEndian.PutUint64(hdrBuf[46:54], uint64(end))
	binary.LittleEndian.PutUint64(hdrBuf[54:62], math.Float64bits(minP))
	if _, err := f.WriteAt(hdrBuf[:], 0); err != nil {
		f.Close()
		return 0, 0, err
	}
	if err := f.Close(); err != nil {
		return 0, 0, err
	}
	return n, clamped, os.Rename(tmp, path)
}
