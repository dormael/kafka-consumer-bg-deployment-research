package com.example.bgtest.consumer.config;

import com.example.bgtest.consumer.listener.PauseAwareRebalanceListener;
import com.example.bgtest.consumer.model.TestMessage;
import org.apache.kafka.clients.consumer.ConsumerConfig;
import org.apache.kafka.common.serialization.StringDeserializer;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.kafka.config.ConcurrentKafkaListenerContainerFactory;
import org.springframework.kafka.core.ConsumerFactory;
import org.springframework.kafka.core.DefaultKafkaConsumerFactory;
import org.springframework.kafka.listener.ContainerProperties;
import org.springframework.kafka.support.serializer.ErrorHandlingDeserializer;
import org.springframework.kafka.support.serializer.JsonDeserializer;

import java.util.HashMap;
import java.util.Map;

@Configuration
public class KafkaConsumerConfig {

    @Value("${spring.kafka.bootstrap-servers}")
    private String bootstrapServers;

    @Value("${spring.kafka.consumer.group-id:bg-test-group}")
    private String groupId;

    @Value("${spring.kafka.consumer.properties.group.instance.id:#{null}}")
    private String groupInstanceId;

    @Value("${spring.kafka.listener.concurrency:1}")
    private int concurrency;

    private final PauseAwareRebalanceListener rebalanceListener;

    public KafkaConsumerConfig(PauseAwareRebalanceListener rebalanceListener) {
        this.rebalanceListener = rebalanceListener;
    }

    @Bean
    public ConsumerFactory<String, TestMessage> consumerFactory() {
        Map<String, Object> props = new HashMap<>();
        props.put(ConsumerConfig.BOOTSTRAP_SERVERS_CONFIG, bootstrapServers);
        props.put(ConsumerConfig.GROUP_ID_CONFIG, groupId);
        props.put(ConsumerConfig.AUTO_OFFSET_RESET_CONFIG, "earliest");
        props.put(ConsumerConfig.ENABLE_AUTO_COMMIT_CONFIG, false);
        props.put(ConsumerConfig.MAX_POLL_RECORDS_CONFIG, 500);
        props.put(ConsumerConfig.MAX_POLL_INTERVAL_MS_CONFIG, 300000);
        props.put(ConsumerConfig.SESSION_TIMEOUT_MS_CONFIG, 45000);
        props.put(ConsumerConfig.HEARTBEAT_INTERVAL_MS_CONFIG, 15000);
        props.put(ConsumerConfig.PARTITION_ASSIGNMENT_STRATEGY_CONFIG,
                "org.apache.kafka.clients.consumer.CooperativeStickyAssignor");

        // Static membership: set group.instance.id if provided
        if (groupInstanceId != null && !groupInstanceId.isEmpty()) {
            props.put(ConsumerConfig.GROUP_INSTANCE_ID_CONFIG, groupInstanceId);
        }

        // Key deserializer
        props.put(ConsumerConfig.KEY_DESERIALIZER_CLASS_CONFIG, StringDeserializer.class);

        // Value deserializer with error handling
        props.put(ConsumerConfig.VALUE_DESERIALIZER_CLASS_CONFIG, ErrorHandlingDeserializer.class);
        props.put(ErrorHandlingDeserializer.VALUE_DESERIALIZER_CLASS, JsonDeserializer.class.getName());
        props.put(JsonDeserializer.TRUSTED_PACKAGES, "com.example.bgtest.*");
        props.put(JsonDeserializer.VALUE_DEFAULT_TYPE, TestMessage.class.getName());
        props.put(JsonDeserializer.USE_TYPE_INFO_HEADERS, false);

        return new DefaultKafkaConsumerFactory<>(props);
    }

    @Bean
    public ConcurrentKafkaListenerContainerFactory<String, TestMessage> kafkaListenerContainerFactory(
            ConsumerFactory<String, TestMessage> consumerFactory) {
        ConcurrentKafkaListenerContainerFactory<String, TestMessage> factory =
                new ConcurrentKafkaListenerContainerFactory<>();
        factory.setConsumerFactory(consumerFactory);
        factory.setConcurrency(concurrency);
        factory.getContainerProperties().setAckMode(ContainerProperties.AckMode.MANUAL_IMMEDIATE);
        factory.getContainerProperties().setConsumerRebalanceListener(rebalanceListener);
        return factory;
    }
}
