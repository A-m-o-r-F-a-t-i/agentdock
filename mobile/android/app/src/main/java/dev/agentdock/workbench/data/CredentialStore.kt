package dev.agentdock.workbench.data

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.nio.charset.StandardCharsets
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/** App-private envelope encryption; tokens are never placed in DataStore, logs, Intents, or Termux arguments. */
class CredentialStore(context: Context) {
    private val preferences = context.getSharedPreferences("agentdock_credentials", Context.MODE_PRIVATE)
    private val keyStore = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }

    fun put(name: String, value: String) {
        require(name in ALLOWED_KEYS)
        if (value.isEmpty()) return clear(name)
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.ENCRYPT_MODE, key())
        val ciphertext = cipher.doFinal(value.toByteArray(StandardCharsets.UTF_8))
        preferences.edit()
            .putString("$name.iv", Base64.encodeToString(cipher.iv, Base64.NO_WRAP))
            .putString("$name.data", Base64.encodeToString(ciphertext, Base64.NO_WRAP))
            .apply()
    }

    fun get(name: String): String {
        require(name in ALLOWED_KEYS)
        val iv = preferences.getString("$name.iv", null) ?: return ""
        val data = preferences.getString("$name.data", null) ?: return ""
        return runCatching {
            val cipher = Cipher.getInstance(TRANSFORMATION)
            cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, Base64.decode(iv, Base64.NO_WRAP)))
            String(cipher.doFinal(Base64.decode(data, Base64.NO_WRAP)), StandardCharsets.UTF_8)
        }.getOrElse { "" }
    }

    fun clear(name: String) {
        require(name in ALLOWED_KEYS)
        preferences.edit().remove("$name.iv").remove("$name.data").apply()
    }

    private fun key(): SecretKey {
        (keyStore.getKey(KEY_ALIAS, null) as? SecretKey)?.let { return it }
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        generator.init(
            KeyGenParameterSpec.Builder(KEY_ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setRandomizedEncryptionRequired(true)
                .build()
        )
        return generator.generateKey()
    }

    companion object {
        private const val KEY_ALIAS = "agentdock.workbench.credentials.v1"
        private const val TRANSFORMATION = "AES/GCM/NoPadding"
        private val ALLOWED_KEYS = setOf("core_bearer", "pairing_secret", "public_access_secret")
    }
}
