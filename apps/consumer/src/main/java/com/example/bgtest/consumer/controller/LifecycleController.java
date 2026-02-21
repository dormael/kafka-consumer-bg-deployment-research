package com.example.bgtest.consumer.controller;

import com.example.bgtest.consumer.model.LifecycleState;
import com.example.bgtest.consumer.service.MessageConsumerService;
import lombok.RequiredArgsConstructor;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;

import java.util.LinkedHashMap;
import java.util.Map;

@RestController
@RequestMapping("/lifecycle")
@RequiredArgsConstructor
public class LifecycleController {

    private final MessageConsumerService consumerService;

    @PostMapping("/pause")
    public ResponseEntity<Map<String, Object>> pause() {
        LifecycleState previousState = consumerService.getLifecycleState();
        consumerService.pause();
        LifecycleState currentState = consumerService.getLifecycleState();

        Map<String, Object> response = new LinkedHashMap<>();
        response.put("previousState", previousState.name());
        response.put("currentState", currentState.name());
        response.put("lastSequenceNumber", consumerService.getLastSequenceNumber());
        return ResponseEntity.ok(response);
    }

    @PostMapping("/resume")
    public ResponseEntity<Map<String, Object>> resume() {
        LifecycleState previousState = consumerService.getLifecycleState();
        consumerService.resume();
        LifecycleState currentState = consumerService.getLifecycleState();

        Map<String, Object> response = new LinkedHashMap<>();
        response.put("previousState", previousState.name());
        response.put("currentState", currentState.name());
        response.put("lastSequenceNumber", consumerService.getLastSequenceNumber());
        return ResponseEntity.ok(response);
    }

    @GetMapping("/status")
    public ResponseEntity<Map<String, Object>> status() {
        Map<String, Object> response = new LinkedHashMap<>();
        response.put("state", consumerService.getLifecycleState().name());
        response.put("stateCode", consumerService.getLifecycleState().getCode());
        response.put("lastSequenceNumber", consumerService.getLastSequenceNumber());
        response.put("totalMessagesReceived", consumerService.getTotalMessagesReceived());
        return ResponseEntity.ok(response);
    }
}
