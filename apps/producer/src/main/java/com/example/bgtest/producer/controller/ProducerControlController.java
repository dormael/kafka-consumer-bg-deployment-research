package com.example.bgtest.producer.controller;

import com.example.bgtest.producer.service.MessageProducerService;
import lombok.RequiredArgsConstructor;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

import java.util.LinkedHashMap;
import java.util.Map;

@RestController
@RequestMapping("/producer")
@RequiredArgsConstructor
public class ProducerControlController {

    private final MessageProducerService producerService;

    @GetMapping("/config")
    public ResponseEntity<Map<String, Object>> getConfig() {
        Map<String, Object> config = new LinkedHashMap<>();
        config.put("producerId", producerService.getProducerId());
        config.put("tps", producerService.getCurrentTps());
        config.put("messageSizeBytes", producerService.getCurrentMessageSize());
        config.put("running", producerService.isRunning());
        return ResponseEntity.ok(config);
    }

    @PutMapping("/config")
    public ResponseEntity<Map<String, Object>> updateConfig(@RequestBody Map<String, Object> request) {
        Long tps = request.containsKey("tps") ? ((Number) request.get("tps")).longValue() : null;
        Long messageSizeBytes = request.containsKey("messageSizeBytes")
                ? ((Number) request.get("messageSizeBytes")).longValue() : null;

        producerService.updateConfig(tps, messageSizeBytes);

        Map<String, Object> response = new LinkedHashMap<>();
        response.put("tps", producerService.getCurrentTps());
        response.put("messageSizeBytes", producerService.getCurrentMessageSize());
        response.put("running", producerService.isRunning());
        return ResponseEntity.ok(response);
    }

    @GetMapping("/stats")
    public ResponseEntity<Map<String, Object>> getStats() {
        Map<String, Object> stats = new LinkedHashMap<>();
        stats.put("producerId", producerService.getProducerId());
        stats.put("running", producerService.isRunning());
        stats.put("totalMessagesSent", producerService.getSequenceCounter());
        stats.put("currentTps", producerService.getCurrentTps());
        stats.put("messageSizeBytes", producerService.getCurrentMessageSize());
        return ResponseEntity.ok(stats);
    }

    @PostMapping("/start")
    public ResponseEntity<Map<String, Object>> start() {
        producerService.start();
        Map<String, Object> response = new LinkedHashMap<>();
        response.put("status", "started");
        response.put("running", producerService.isRunning());
        return ResponseEntity.ok(response);
    }

    @PostMapping("/stop")
    public ResponseEntity<Map<String, Object>> stop() {
        producerService.stop();
        Map<String, Object> response = new LinkedHashMap<>();
        response.put("status", "stopped");
        response.put("running", producerService.isRunning());
        response.put("lastSequenceNumber", producerService.getSequenceCounter());
        return ResponseEntity.ok(response);
    }
}
