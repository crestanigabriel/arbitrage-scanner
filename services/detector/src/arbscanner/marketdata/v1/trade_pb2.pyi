import datetime

from google.protobuf import timestamp_pb2 as _timestamp_pb2
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class Side(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    SIDE_UNSPECIFIED: _ClassVar[Side]
    SIDE_BUY: _ClassVar[Side]
    SIDE_SELL: _ClassVar[Side]
SIDE_UNSPECIFIED: Side
SIDE_BUY: Side
SIDE_SELL: Side

class Trade(_message.Message):
    __slots__ = ("exchange", "symbol", "trade_id", "price", "quantity", "taker_side", "exchange_ts", "received_ts", "published_ts")
    EXCHANGE_FIELD_NUMBER: _ClassVar[int]
    SYMBOL_FIELD_NUMBER: _ClassVar[int]
    TRADE_ID_FIELD_NUMBER: _ClassVar[int]
    PRICE_FIELD_NUMBER: _ClassVar[int]
    QUANTITY_FIELD_NUMBER: _ClassVar[int]
    TAKER_SIDE_FIELD_NUMBER: _ClassVar[int]
    EXCHANGE_TS_FIELD_NUMBER: _ClassVar[int]
    RECEIVED_TS_FIELD_NUMBER: _ClassVar[int]
    PUBLISHED_TS_FIELD_NUMBER: _ClassVar[int]
    exchange: str
    symbol: str
    trade_id: str
    price: str
    quantity: str
    taker_side: Side
    exchange_ts: _timestamp_pb2.Timestamp
    received_ts: _timestamp_pb2.Timestamp
    published_ts: _timestamp_pb2.Timestamp
    def __init__(self, exchange: _Optional[str] = ..., symbol: _Optional[str] = ..., trade_id: _Optional[str] = ..., price: _Optional[str] = ..., quantity: _Optional[str] = ..., taker_side: _Optional[_Union[Side, str]] = ..., exchange_ts: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., received_ts: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., published_ts: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...
