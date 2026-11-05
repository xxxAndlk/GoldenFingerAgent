package com.goldenfinger.butler

import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import gosvc.SecretStore
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * Android Keystore 加密桥。AES 密钥不可导出，Go 只提交待加密/解密的数据；
 * 密文由 Go 写入 App 私有目录，文件中没有明文 Token，也没有密钥材料。
 */
class AndroidSecretStore : SecretStore {
    private val store = KeyStore.getInstance(PROVIDER).apply { load(null) }

    @Throws(Exception::class)
    override fun encrypt(plain: ByteArray): ByteArray {
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.ENCRYPT_MODE, key())
        val iv = cipher.iv
        val encrypted = cipher.doFinal(plain)
        require(iv.size <= 255) { "IV too long" }
        return byteArrayOf(iv.size.toByte()) + iv + encrypted
    }

    @Throws(Exception::class)
    override fun decrypt(ciphertext: ByteArray): ByteArray {
        require(ciphertext.size > 1) { "Ciphertext is empty" }
        val ivSize = ciphertext[0].toInt() and 0xff
        require(ivSize > 0 && ciphertext.size > 1 + ivSize) { "Ciphertext is invalid" }
        val iv = ciphertext.copyOfRange(1, 1 + ivSize)
        val encrypted = ciphertext.copyOfRange(1 + ivSize, ciphertext.size)
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(GCM_TAG_BITS, iv))
        return cipher.doFinal(encrypted)
    }

    private fun key(): SecretKey {
        store.getKey(ALIAS, null)?.let { return it as SecretKey }
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, PROVIDER)
        generator.init(
            KeyGenParameterSpec.Builder(
                ALIAS,
                KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT
            )
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setRandomizedEncryptionRequired(true)
                .setKeySize(256)
                .build()
        )
        return generator.generateKey()
    }

    private companion object {
        const val PROVIDER = "AndroidKeyStore"
        const val ALIAS = "gfa.cloud-token.v1"
        const val TRANSFORMATION = "AES/GCM/NoPadding"
        const val GCM_TAG_BITS = 128
    }
}
