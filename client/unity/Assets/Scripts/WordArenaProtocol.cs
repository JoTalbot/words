using System;
using System.Collections.Generic;
using System.IO;
using System.Text;

namespace Words.Client
{
    public enum WordArenaWordResult
    {
        Unspecified = 0,
        Accepted = 1,
        RejectedNotInDictionary = 2,
        AlreadyClaimed = 3,
        BlockedByRule = 4,
        InvalidInput = 5,
        MatchNotActive = 6
    }

    public sealed class WordArenaServerEnvelope
    {
        public ulong MatchId;
        public WordArenaValidatedEvent WordEvent;
        public WordArenaSnapshot Snapshot;

        public bool HasWordEvent { get { return WordEvent != null; } }
        public bool HasSnapshot { get { return Snapshot != null; } }
    }

    public sealed class WordArenaValidatedEvent
    {
        public ulong EventId;
        public uint ServerTick;
        public uint ClientSequence;
        public WordArenaWordResult Result;
        public string NormalizedWord = string.Empty;
        public uint ScoreAdded;
        public uint TotalScore;
        public float ComboMultiplier;
        public uint ClaimedCellIndex;
        public bool IsSteal;
        public uint StateVersion;
        public ulong UserId;

        public bool Accepted { get { return Result == WordArenaWordResult.Accepted; } }
    }

    public sealed class WordArenaSnapshot
    {
        public ulong MatchId;
        public uint ServerTick;
        public uint RemainingTimeMs;
        public uint CurrentWave;
        public uint StateVersion;
        public bool Over;
        public readonly List<WordArenaPlayerState> Players = new List<WordArenaPlayerState>();
        public readonly List<WordArenaBoardCell> Cells = new List<WordArenaBoardCell>();
    }

    public sealed class WordArenaPlayerState
    {
        public ulong UserId;
        public uint Score;
        public uint RankPosition;
        public bool IsEliminated;
        public float ComboMultiplier;
    }

    public sealed class WordArenaBoardCell
    {
        public uint CellId;
        public string Letter = string.Empty;
        public ulong OwnerUserId;
        public bool IsLocked;
        public uint LockRemainingMs;
    }

    /// <summary>
    /// Tiny protobuf adapter for proto/wordarena/v1/match.proto.
    ///
    /// Unity CI intentionally keeps the project package-light, so the client
    /// bootstrap decodes only the authoritative message subset it currently
    /// needs instead of adding a generated protobuf runtime. Unknown fields are
    /// skipped, preserving forward compatibility for the existing binary WS
    /// contract. Competitive truth still comes only from server messages.
    /// </summary>
    public static class WordArenaProto
    {
        public static byte[] EncodeSubmitWord(ulong matchId, uint clientSequence, IList<int> letterIndices, ulong clientTimestampMs)
        {
            if (letterIndices == null)
            {
                throw new ArgumentNullException("letterIndices");
            }

            using (var intent = new MemoryStream())
            {
                WriteVarintField(intent, 1, matchId);
                WriteVarintField(intent, 2, clientSequence);
                for (var index = 0; index < letterIndices.Count; index++)
                {
                    var cell = letterIndices[index];
                    if (cell < 0)
                    {
                        throw new ArgumentOutOfRangeException("letterIndices", "cell index must be non-negative");
                    }

                    // Proto3 repeated numeric fields accept both packed and
                    // unpacked encodings. Unpacked keeps this scoped encoder
                    // simple and the Go server unmarshals it equivalently.
                    WriteVarintField(intent, 3, (ulong)cell);
                }

                if (clientTimestampMs > 0)
                {
                    WriteVarintField(intent, 5, clientTimestampMs);
                }

                var intentBytes = intent.ToArray();
                using (var envelope = new MemoryStream())
                {
                    WriteVarintField(envelope, 1, matchId);
                    WriteBytesField(envelope, 2, intentBytes);
                    return envelope.ToArray();
                }
            }
        }

        public static bool TryDecodeServerEnvelope(byte[] data, int length, out WordArenaServerEnvelope envelope, out string error)
        {
            envelope = new WordArenaServerEnvelope();
            error = string.Empty;
            if (data == null)
            {
                error = "payload is null";
                return false;
            }

            var position = 0;
            var end = Math.Min(length, data.Length);
            while (position < end)
            {
                int field;
                int wire;
                if (!ReadTag(data, end, ref position, out field, out wire, out error))
                {
                    return false;
                }

                if (field == 1 && wire == 0)
                {
                    ulong value;
                    if (!ReadVarint(data, end, ref position, out value, out error))
                    {
                        return false;
                    }

                    envelope.MatchId = value;
                    continue;
                }

                if (field == 2 && wire == 2)
                {
                    int start;
                    int count;
                    if (!ReadLengthDelimited(data, end, ref position, out start, out count, out error))
                    {
                        return false;
                    }

                    WordArenaValidatedEvent wordEvent;
                    if (!TryDecodeWordEvent(data, start, start + count, out wordEvent, out error))
                    {
                        return false;
                    }

                    envelope.WordEvent = wordEvent;
                    continue;
                }

                if (field == 3 && wire == 2)
                {
                    int start;
                    int count;
                    if (!ReadLengthDelimited(data, end, ref position, out start, out count, out error))
                    {
                        return false;
                    }

                    WordArenaSnapshot snapshot;
                    if (!TryDecodeSnapshot(data, start, start + count, out snapshot, out error))
                    {
                        return false;
                    }

                    envelope.Snapshot = snapshot;
                    continue;
                }

                if (!SkipField(data, end, ref position, wire, out error))
                {
                    return false;
                }
            }

            if (envelope.Snapshot != null && envelope.Snapshot.MatchId == 0)
            {
                envelope.Snapshot.MatchId = envelope.MatchId;
            }

            return envelope.HasSnapshot || envelope.HasWordEvent;
        }

        private static bool TryDecodeWordEvent(byte[] data, int position, int end, out WordArenaValidatedEvent wordEvent, out string error)
        {
            wordEvent = new WordArenaValidatedEvent();
            error = string.Empty;
            while (position < end)
            {
                int field;
                int wire;
                if (!ReadTag(data, end, ref position, out field, out wire, out error))
                {
                    return false;
                }

                ulong value;
                switch (field)
                {
                    case 1:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        wordEvent.EventId = value;
                        break;
                    case 2:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        wordEvent.ServerTick = (uint)value;
                        break;
                    case 3:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        wordEvent.ClientSequence = (uint)value;
                        break;
                    case 4:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        wordEvent.Result = (WordArenaWordResult)value;
                        break;
                    case 5:
                        if (!RequireWire(wire, 2, out error) || !ReadString(data, end, ref position, out wordEvent.NormalizedWord, out error)) return false;
                        break;
                    case 6:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        wordEvent.ScoreAdded = (uint)value;
                        break;
                    case 7:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        wordEvent.TotalScore = (uint)value;
                        break;
                    case 8:
                        if (!RequireWire(wire, 5, out error) || !ReadFloat32(data, end, ref position, out wordEvent.ComboMultiplier, out error)) return false;
                        break;
                    case 9:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        wordEvent.ClaimedCellIndex = (uint)value;
                        break;
                    case 10:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        wordEvent.IsSteal = value != 0;
                        break;
                    case 11:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        wordEvent.StateVersion = (uint)value;
                        break;
                    case 12:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        wordEvent.UserId = value;
                        break;
                    default:
                        if (!SkipField(data, end, ref position, wire, out error)) return false;
                        break;
                }
            }

            return true;
        }

        private static bool TryDecodeSnapshot(byte[] data, int position, int end, out WordArenaSnapshot snapshot, out string error)
        {
            snapshot = new WordArenaSnapshot();
            error = string.Empty;
            while (position < end)
            {
                int field;
                int wire;
                if (!ReadTag(data, end, ref position, out field, out wire, out error))
                {
                    return false;
                }

                ulong value;
                switch (field)
                {
                    case 1:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        snapshot.MatchId = value;
                        break;
                    case 2:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        snapshot.ServerTick = (uint)value;
                        break;
                    case 3:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        snapshot.RemainingTimeMs = (uint)value;
                        break;
                    case 4:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        snapshot.CurrentWave = (uint)value;
                        break;
                    case 5:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        snapshot.StateVersion = (uint)value;
                        break;
                    case 6:
                        if (!RequireWire(wire, 2, out error)) return false;
                        int playerStart;
                        int playerCount;
                        if (!ReadLengthDelimited(data, end, ref position, out playerStart, out playerCount, out error)) return false;
                        WordArenaPlayerState player;
                        if (!TryDecodePlayer(data, playerStart, playerStart + playerCount, out player, out error)) return false;
                        snapshot.Players.Add(player);
                        break;
                    case 7:
                        if (!RequireWire(wire, 2, out error)) return false;
                        int cellStart;
                        int cellCount;
                        if (!ReadLengthDelimited(data, end, ref position, out cellStart, out cellCount, out error)) return false;
                        WordArenaBoardCell cell;
                        if (!TryDecodeCell(data, cellStart, cellStart + cellCount, out cell, out error)) return false;
                        snapshot.Cells.Add(cell);
                        break;
                    case 8:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        snapshot.Over = value != 0;
                        break;
                    default:
                        if (!SkipField(data, end, ref position, wire, out error)) return false;
                        break;
                }
            }

            return true;
        }

        private static bool TryDecodePlayer(byte[] data, int position, int end, out WordArenaPlayerState player, out string error)
        {
            player = new WordArenaPlayerState();
            error = string.Empty;
            while (position < end)
            {
                int field;
                int wire;
                if (!ReadTag(data, end, ref position, out field, out wire, out error))
                {
                    return false;
                }

                ulong value;
                switch (field)
                {
                    case 1:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        player.UserId = value;
                        break;
                    case 2:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        player.Score = (uint)value;
                        break;
                    case 3:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        player.RankPosition = (uint)value;
                        break;
                    case 4:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        player.IsEliminated = value != 0;
                        break;
                    case 5:
                        if (!RequireWire(wire, 5, out error) || !ReadFloat32(data, end, ref position, out player.ComboMultiplier, out error)) return false;
                        break;
                    default:
                        if (!SkipField(data, end, ref position, wire, out error)) return false;
                        break;
                }
            }

            return true;
        }

        private static bool TryDecodeCell(byte[] data, int position, int end, out WordArenaBoardCell cell, out string error)
        {
            cell = new WordArenaBoardCell();
            error = string.Empty;
            while (position < end)
            {
                int field;
                int wire;
                if (!ReadTag(data, end, ref position, out field, out wire, out error))
                {
                    return false;
                }

                ulong value;
                switch (field)
                {
                    case 1:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        cell.CellId = (uint)value;
                        break;
                    case 2:
                        if (!RequireWire(wire, 2, out error) || !ReadString(data, end, ref position, out cell.Letter, out error)) return false;
                        break;
                    case 3:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        cell.OwnerUserId = value;
                        break;
                    case 4:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        cell.IsLocked = value != 0;
                        break;
                    case 5:
                        if (!RequireWire(wire, 0, out error) || !ReadVarint(data, end, ref position, out value, out error)) return false;
                        cell.LockRemainingMs = (uint)value;
                        break;
                    default:
                        if (!SkipField(data, end, ref position, wire, out error)) return false;
                        break;
                }
            }

            return true;
        }

        private static void WriteVarintField(MemoryStream stream, int fieldNumber, ulong value)
        {
            WriteVarint(stream, ((ulong)fieldNumber << 3) | 0UL);
            WriteVarint(stream, value);
        }

        private static void WriteBytesField(MemoryStream stream, int fieldNumber, byte[] value)
        {
            WriteVarint(stream, ((ulong)fieldNumber << 3) | 2UL);
            WriteVarint(stream, (ulong)value.Length);
            stream.Write(value, 0, value.Length);
        }

        private static void WriteVarint(MemoryStream stream, ulong value)
        {
            while (value >= 0x80UL)
            {
                stream.WriteByte((byte)(value | 0x80UL));
                value >>= 7;
            }

            stream.WriteByte((byte)value);
        }

        private static bool ReadTag(byte[] data, int end, ref int position, out int field, out int wire, out string error)
        {
            field = 0;
            wire = 0;
            ulong tag;
            if (!ReadVarint(data, end, ref position, out tag, out error))
            {
                return false;
            }

            field = (int)(tag >> 3);
            wire = (int)(tag & 0x07UL);
            if (field <= 0)
            {
                error = "invalid protobuf field number";
                return false;
            }

            return true;
        }

        private static bool ReadVarint(byte[] data, int end, ref int position, out ulong value, out string error)
        {
            value = 0UL;
            error = string.Empty;
            var shift = 0;
            while (position < end && shift < 64)
            {
                var b = data[position++];
                value |= ((ulong)(b & 0x7F)) << shift;
                if ((b & 0x80) == 0)
                {
                    return true;
                }

                shift += 7;
            }

            error = "truncated or oversized protobuf varint";
            return false;
        }

        private static bool ReadLengthDelimited(byte[] data, int end, ref int position, out int start, out int count, out string error)
        {
            start = 0;
            count = 0;
            ulong length;
            if (!ReadVarint(data, end, ref position, out length, out error))
            {
                return false;
            }

            if (length > int.MaxValue || position + (int)length > end)
            {
                error = "truncated protobuf length-delimited field";
                return false;
            }

            start = position;
            count = (int)length;
            position += count;
            return true;
        }

        private static bool ReadString(byte[] data, int end, ref int position, out string value, out string error)
        {
            value = string.Empty;
            int start;
            int count;
            if (!ReadLengthDelimited(data, end, ref position, out start, out count, out error))
            {
                return false;
            }

            value = Encoding.UTF8.GetString(data, start, count);
            return true;
        }

        private static bool ReadFloat32(byte[] data, int end, ref int position, out float value, out string error)
        {
            value = 0f;
            error = string.Empty;
            if (position + 4 > end)
            {
                error = "truncated protobuf fixed32 field";
                return false;
            }

            var bytes = new byte[4];
            Buffer.BlockCopy(data, position, bytes, 0, 4);
            if (!BitConverter.IsLittleEndian)
            {
                Array.Reverse(bytes);
            }

            value = BitConverter.ToSingle(bytes, 0);
            position += 4;
            return true;
        }

        private static bool SkipField(byte[] data, int end, ref int position, int wire, out string error)
        {
            error = string.Empty;
            ulong value;
            switch (wire)
            {
                case 0:
                    return ReadVarint(data, end, ref position, out value, out error);
                case 1:
                    if (position + 8 > end)
                    {
                        error = "truncated protobuf fixed64 field";
                        return false;
                    }

                    position += 8;
                    return true;
                case 2:
                    int start;
                    int count;
                    return ReadLengthDelimited(data, end, ref position, out start, out count, out error);
                case 5:
                    if (position + 4 > end)
                    {
                        error = "truncated protobuf fixed32 field";
                        return false;
                    }

                    position += 4;
                    return true;
                default:
                    error = "unsupported protobuf wire type " + wire;
                    return false;
            }
        }

        private static bool RequireWire(int actual, int expected, out string error)
        {
            if (actual == expected)
            {
                error = string.Empty;
                return true;
            }

            error = "unexpected protobuf wire type " + actual + ", expected " + expected;
            return false;
        }
    }
}
