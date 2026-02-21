package com.example.bgtest.producer.model;

import com.fasterxml.jackson.annotation.JsonFormat;
import com.fasterxml.jackson.annotation.JsonProperty;
import lombok.AllArgsConstructor;
import lombok.Builder;
import lombok.Data;
import lombok.NoArgsConstructor;

import java.time.Instant;

@Data
@Builder
@NoArgsConstructor
@AllArgsConstructor
public class TestMessage {

    @JsonProperty("sequenceNumber")
    private long sequenceNumber;

    @JsonProperty("producerId")
    private String producerId;

    @JsonProperty("timestamp")
    @JsonFormat(shape = JsonFormat.Shape.STRING, pattern = "yyyy-MM-dd'T'HH:mm:ss.SSS'Z'", timezone = "UTC")
    private Instant timestamp;

    @JsonProperty("partition")
    private int partition;

    @JsonProperty("payload")
    private byte[] payload;
}
