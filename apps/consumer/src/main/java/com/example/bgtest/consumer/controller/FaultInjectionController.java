package com.example.bgtest.consumer.controller;

import com.example.bgtest.consumer.config.FaultInjectionConfig;
import com.example.bgtest.consumer.service.FaultInjectionService;
import lombok.RequiredArgsConstructor;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PutMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

import java.util.LinkedHashMap;
import java.util.Map;

@RestController
@RequestMapping("/fault")
@RequiredArgsConstructor
public class FaultInjectionController {

    private final FaultInjectionService faultInjectionService;

    @GetMapping
    public ResponseEntity<FaultInjectionConfig> getConfig() {
        return ResponseEntity.ok(faultInjectionService.getConfig());
    }

    @PutMapping("/processing-delay")
    public ResponseEntity<Map<String, Object>> setProcessingDelay(@RequestBody Map<String, Object> request) {
        long delayMs = ((Number) request.get("delayMs")).longValue();
        faultInjectionService.setProcessingDelayMs(delayMs);

        Map<String, Object> response = new LinkedHashMap<>();
        response.put("type", "processing-delay");
        response.put("delayMs", delayMs);
        response.put("status", "applied");
        return ResponseEntity.ok(response);
    }

    @PutMapping("/error-rate")
    public ResponseEntity<Map<String, Object>> setErrorRate(@RequestBody Map<String, Object> request) {
        int errorRatePercent = ((Number) request.get("errorRatePercent")).intValue();
        faultInjectionService.setErrorRatePercent(errorRatePercent);

        Map<String, Object> response = new LinkedHashMap<>();
        response.put("type", "error-rate");
        response.put("errorRatePercent", errorRatePercent);
        response.put("status", "applied");
        return ResponseEntity.ok(response);
    }

    @PutMapping("/commit-delay")
    public ResponseEntity<Map<String, Object>> setCommitDelay(@RequestBody Map<String, Object> request) {
        long delayMs = ((Number) request.get("delayMs")).longValue();
        faultInjectionService.setCommitDelayMs(delayMs);

        Map<String, Object> response = new LinkedHashMap<>();
        response.put("type", "commit-delay");
        response.put("delayMs", delayMs);
        response.put("status", "applied");
        return ResponseEntity.ok(response);
    }

    @PutMapping("/poll-timeout")
    public ResponseEntity<Map<String, Object>> setPollTimeout(@RequestBody Map<String, Object> request) {
        boolean enabled = (Boolean) request.get("enabled");
        faultInjectionService.setPollTimeoutExceed(enabled);

        Map<String, Object> response = new LinkedHashMap<>();
        response.put("type", "poll-timeout-exceed");
        response.put("enabled", enabled);
        response.put("status", "applied");
        return ResponseEntity.ok(response);
    }
}
