package com.example.bgtest.consumer.model;

public enum LifecycleState {

    ACTIVE(0),
    DRAINING(1),
    PAUSED(2);

    private final int code;

    LifecycleState(int code) {
        this.code = code;
    }

    public int getCode() {
        return code;
    }
}
